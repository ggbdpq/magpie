package gateway

// canEnter is true on Windows: the folder is the caller's own to enter,
// and the access bits Go reports there are not the ones a run asks for.
func canEnter(string) bool { return true }
