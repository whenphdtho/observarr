package auth

import "strings"

// common are the long passwords tried first by anyone guessing: a short list, lower case. Length is the rule that
// matters; this only refuses the few that are long and still everyone's first guess.
var common = map[string]bool{
	"123456789012": true, "1234567890123": true, "12345678901234": true, "012345678901": true,
	"password1234": true, "password12345": true, "passwordpassword": true, "password123456": true,
	"qwertyuiop12": true, "qwertyuiop123": true, "qwertyuiopas": true, "qwertyuiopasdf": true,
	"iloveyou1234": true, "administrator": true, "adminadminadmin": true, "letmeinletmein": true,
	"changemechangeme": true, "welcome12345": true, "abcdefghijkl": true, "abcdefghijklmnop": true,
}

// WeakPassword reports whether password, whatever its length, is one a guesser tries at once: one character
// repeated, or one of the most common long passwords (in any case).
func WeakPassword(password string) bool {
	if password != "" && strings.Trim(password, password[:1]) == "" {
		return true
	}
	return common[strings.ToLower(password)]
}
