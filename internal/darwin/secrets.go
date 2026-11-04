//go:build darwin

package darwin

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/platform"
)

// secrets keeps secrets as generic passwords of the login Keychain, the
// service and account as keytar does. It uses the file-based keychain:
// the data protection one needs a signed app with a keychain-access-groups
// entitlement (unsigned builds get errSecMissingEntitlement).
type secrets struct{}

const (
	errSecItemNotFound  = -25300
	errSecDuplicateItem = -25299
)

// The Security framework, loaded with the first secret.
var keychain = sync.OnceValue(func() *keychainAPI {
	lib, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return &keychainAPI{err: fmt.Errorf("cannot load the Security framework: %w", err)}
	}
	k := &keychainAPI{
		add:     mustDlsym(lib, "SecItemAdd"),
		copy:    mustDlsym(lib, "SecItemCopyMatching"),
		update:  mustDlsym(lib, "SecItemUpdate"),
		delete:  mustDlsym(lib, "SecItemDelete"),
		message: mustDlsym(lib, "SecCopyErrorMessageString"),
	}
	k.class = constString(lib, "kSecClass")
	k.genericPassword = constString(lib, "kSecClassGenericPassword")
	k.service = constString(lib, "kSecAttrService")
	k.account = constString(lib, "kSecAttrAccount")
	k.label = constString(lib, "kSecAttrLabel")
	k.valueData = constString(lib, "kSecValueData")
	k.returnData = constString(lib, "kSecReturnData")
	k.matchLimit = constString(lib, "kSecMatchLimit")
	k.matchLimitOne = constString(lib, "kSecMatchLimitOne")
	k.cfTrue = constString(libCF, "kCFBooleanTrue")
	return k
})

type keychainAPI struct {
	err                                error
	add, copy, update, delete, message uintptr
	class, genericPassword             id
	service, account, label            id
	valueData, returnData              id
	matchLimit, matchLimitOne, cfTrue  id
}

// query returns an owned dictionary that finds the item of service and
// account, which the caller releases.
func (k *keychainAPI) query(service, account string) id {
	q := newDictionary()
	setObject(q, k.genericPassword, k.class)
	setObject(q, nsString(service), k.service)
	setObject(q, nsString(account), k.account)
	return q
}

// status converts an OSStatus into an error with the system's message.
func (k *keychainAPI) status(op string, s int32) error {
	switch s {
	case 0:
		return nil
	case errSecItemNotFound:
		return platform.ErrSecretNotFound
	}
	msg := ""
	if str, _, _ := purego.SyscallN(k.message, uintptr(s), 0); str != 0 {
		msg = goString(id(str))
		cfRelease(str)
	}
	if msg == "" {
		msg = "unknown error"
	}
	return fmt.Errorf("keychain: %s: %s (OSStatus %d)", op, msg, s)
}

func osStatus(r uintptr) int32 { return int32(uint32(r)) }

func (secrets) SetSecret(service, account string, secret []byte) (err error) {
	k := keychain()
	if k.err != nil {
		return k.err
	}
	withPool(func() {
		data := nsData(secret)
		update := newDictionary()
		defer release(update)
		setObject(update, data, k.valueData)
		q := k.query(service, account)
		defer release(q)
		r, _, _ := purego.SyscallN(k.update, uintptr(q), uintptr(update))
		if s := osStatus(r); s != errSecItemNotFound {
			err = k.status("update", s)
			return
		}
		add := k.query(service, account)
		defer release(add)
		setObject(add, data, k.valueData)
		setObject(add, nsString(service), k.label)
		r, _, _ = purego.SyscallN(k.add, uintptr(add), 0)
		if s := osStatus(r); s == errSecDuplicateItem {
			// Added by another thread or process since the update.
			r, _, _ = purego.SyscallN(k.update, uintptr(q), uintptr(update))
			err = k.status("update", osStatus(r))
		} else {
			err = k.status("add", s)
		}
	})
	return err
}

func (secrets) Secret(service, account string) (secret []byte, err error) {
	k := keychain()
	if k.err != nil {
		return nil, k.err
	}
	withPool(func() {
		q := k.query(service, account)
		defer release(q)
		setObject(q, k.cfTrue, k.returnData)
		setObject(q, k.matchLimitOne, k.matchLimit)
		var data uintptr
		r, _, _ := purego.SyscallN(k.copy, uintptr(q), uintptr(unsafe.Pointer(&data)))
		if err = k.status("read", osStatus(r)); err != nil {
			return
		}
		if data == 0 {
			err = errors.New("keychain: read: no data")
			return
		}
		secret = cfDataBytes(data)
		cfRelease(data)
	})
	return secret, err
}

func (secrets) DeleteSecret(service, account string) (err error) {
	k := keychain()
	if k.err != nil {
		return k.err
	}
	withPool(func() {
		q := k.query(service, account)
		defer release(q)
		r, _, _ := purego.SyscallN(k.delete, uintptr(q))
		if err = k.status("delete", osStatus(r)); errors.Is(err, platform.ErrSecretNotFound) {
			err = nil
		}
	})
	return err
}
