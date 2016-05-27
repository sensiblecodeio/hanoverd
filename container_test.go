package main

import "testing"

func TestImageRef(t *testing.T) {
	data := map[string][]string{
		"http://user:pass@localhost.localdomain:5000/org/hanoverd:master-0-g1234567": []string{
			"http://user:pass@localhost.localdomain:5000/org/hanoverd", "master-0-g1234567",
		},
		"http://user:pass@localhost.localdomain:5000/hanoverd:master-0-g1234567": []string{
			"http://user:pass@localhost.localdomain:5000/hanoverd", "master-0-g1234567",
		},
		"http://localhost.localdomain:5000/hanoverd:master-0-g1234567": []string{
			"http://localhost.localdomain:5000/hanoverd", "master-0-g1234567",
		},
		"localhost.localdomain:5000/hanoverd:master-0-g1234567": []string{
			"localhost.localdomain:5000/hanoverd", "master-0-g1234567",
		},
		"localhost.localdomain:5000/hanoverd@0123456789abcdef": []string{
			"localhost.localdomain:5000/hanoverd", "0123456789abcdef",
		},
		"localhost.localdomain:5000/hanoverd": []string{
			"localhost.localdomain:5000/hanoverd", "latest",
		},
		"localhost.localdomain/hanoverd:master-0-g1234567": []string{
			"localhost.localdomain/hanoverd", "master-0-g1234567",
		},
		"localhost.localdomain/hanoverd@0123456789abcdef": []string{
			"localhost.localdomain/hanoverd", "0123456789abcdef",
		},
		"localhost.localdomain/hanoverd": []string{
			"localhost.localdomain/hanoverd", "latest",
		},
		"hanoverd:master-0-g1234567": []string{
			"hanoverd", "master-0-g1234567",
		},
		"hanoverd@0123456789abcdef": []string{
			"hanoverd", "0123456789abcdef",
		},
		"hanoverd": []string{
			"hanoverd", "latest",
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
