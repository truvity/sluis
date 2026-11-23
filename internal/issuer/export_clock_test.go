package issuer

import "time"

// SetStorageClock replaces the clock a storage reads, for the tests of what
// ends with time (a previous client secret).
func SetStorageClock(s *Storage, now func() time.Time) { s.now = now }

// SetDeviceDecided runs hook between a device decision's read and its write.
func SetDeviceDecided(s *Storage, hook func()) { s.deviceDecided = hook }
