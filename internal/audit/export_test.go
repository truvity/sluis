package audit

// SetUnsupportedDataHook replaces what is told of a data value of a type the
// record builder does not handle, and returns what to restore.
func SetUnsupportedDataHook(f func(key string, v any)) (restore func()) {
	old := unsupportedData.Swap(&f)
	return func() { unsupportedData.Store(old) }
}
