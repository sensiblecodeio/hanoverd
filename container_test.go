package main

import "testing"

func TestImageRef(t *testing.T) {
	data := map[string][]string{
		"http://user:pass@localhost.localdomain:5000/org/pdftables.com:master-0-g1234567": []string{
			"http://user:pass@localhost.localdomain:5000/org/pdftables.com", "master-0-g1234567",
		},
		"http://user:pass@localhost.localdomain:5000/pdftables.com:master-0-g1234567": []string{
			"http://user:pass@localhost.localdomain:5000/pdftables.com", "master-0-g1234567",
		},
		"http://localhost.localdomain:5000/pdftables.com:master-0-g1234567": []string{
			"http://localhost.localdomain:5000/pdftables.com", "master-0-g1234567",
		},
		"localhost.localdomain:5000/pdftables.com:master-0-g1234567": []string{
			"localhost.localdomain:5000/pdftables.com", "master-0-g1234567",
		},
		"localhost.localdomain:5000/pdftables.com@0123456789abcdef": []string{
			"localhost.localdomain:5000/pdftables.com", "0123456789abcdef",
		},
		"localhost.localdomain:5000/pdftables.com": []string{
			"localhost.localdomain:5000/pdftables.com", "latest",
		},
		"localhost.localdomain/pdftables.com:master-0-g1234567": []string{
			"localhost.localdomain/pdftables.com", "master-0-g1234567",
		},
		"localhost.localdomain/pdftables.com@0123456789abcdef": []string{
			"localhost.localdomain/pdftables.com", "0123456789abcdef",
		},
		"localhost.localdomain/pdftables.com": []string{
			"localhost.localdomain/pdftables.com", "latest",
		},
		"pdftables.com:master-0-g1234567": []string{
			"pdftables.com", "master-0-g1234567",
		},
		"pdftables.com@0123456789abcdef": []string{
			"pdftables.com", "0123456789abcdef",
		},
		"pdftables.com": []string{
			"pdftables.com", "latest",
		},
		"": []string{
			"", "latest",
		},
	}

	for input, expected := range data {
		givenName, givenTagDigest := imageRef(input)
		if givenName != expected[0] || givenTagDigest != expected[1] {
			t.Errorf("Expected: %s %s but got %s %s", expected[0], expected[1], givenName, givenTagDigest)
		}
	}
}
