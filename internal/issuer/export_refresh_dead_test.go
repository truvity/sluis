package issuer

// SessionTokenKeyForTest is where a refresh token's pointer is kept, so that
// a test can tell whether ending a session removed it.
func SessionTokenKeyForTest(token string) string { return sessionTokenKey(token) }
