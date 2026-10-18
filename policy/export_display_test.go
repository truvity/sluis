package policy

// DisplayTextForTest is the check a declared client's display text is held to.
func DisplayTextForTest(field, value string, limit int) error {
	return displayText(field, value, limit)
}
