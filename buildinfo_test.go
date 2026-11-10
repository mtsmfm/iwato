package main

import "testing"

func TestImageRef(t *testing.T) {
	defer func(tag, d string) { imageTag, imageDigests = tag, d }(imageTag, imageDigests)
	if got := imageRef("iwato-sandbox"); got != "ghcr.io/mtsmfm/iwato-sandbox:edge" || !imageMoves() {
		t.Fatal("the default pulls edge:", got)
	}
	imageTag = ""
	if got := imageRef("iwato-gateway"); got != "ghcr.io/mtsmfm/iwato-gateway:"+version || imageMoves() {
		t.Fatal(got)
	}
	imageDigests = "iwato-gateway=sha256:aa,iwato-sandbox=sha256:bb"
	if got := imageRef("iwato-sandbox"); got != "ghcr.io/mtsmfm/iwato-sandbox:"+version+"@sha256:bb" {
		t.Fatal(got)
	}
	imageTag = "edge"
	if got := imageRef("iwato-gateway"); got != "ghcr.io/mtsmfm/iwato-gateway:edge@sha256:aa" || imageMoves() {
		t.Fatal(got)
	}
}
