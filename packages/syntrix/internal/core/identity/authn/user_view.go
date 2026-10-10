package authn

import (
	"reflect"
	"slices"

	"github.com/codetreker/syntrix/internal/identity"
)

func userView(user *User) *identity.User {
	view := &identity.User{
		ID: user.ID, Username: user.Username, CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt,
		Disabled: user.Disabled, Roles: slices.Clone(user.Roles), DBAdmin: slices.Clone(user.DBAdmin),
		LastLoginAt: user.LastLoginAt, LoginAttempts: user.LoginAttempts, LockoutUntil: user.LockoutUntil,
	}
	if user.Profile != nil {
		view.Profile = cloneProfileValue(reflect.ValueOf(user.Profile)).Interface().(map[string]interface{})
	}
	return view
}

// Profile values can include typed maps and slices from storage decoders. Copy
// their concrete shapes so account views do not share mutable persistence data.
func cloneProfileValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		copy := reflect.New(value.Type()).Elem()
		copy.Set(cloneProfileValue(value.Elem()))
		return copy
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		for iterator := value.MapRange(); iterator.Next(); {
			copy.SetMapIndex(iterator.Key(), cloneProfileValue(iterator.Value()))
		}
		return copy
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			copy.Index(i).Set(cloneProfileValue(value.Index(i)))
		}
		return copy
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			copy.Index(i).Set(cloneProfileValue(value.Index(i)))
		}
		return copy
	default:
		return value
	}
}
