package main

import "testing"

func TestImageRef(t *testing.T) {
	defer func(r, tag string) { imageRegistry, imageTag = r, tag }(imageRegistry, imageTag)
	imageRegistry, imageTag = "", ""
	if got := imageRef("iwato-sandbox"); got != "iwato-sandbox:"+version {
		t.Fatal(got)
	}
	imageRegistry = "ghcr.io/mtsmfm"
	if got := imageRef("iwato-gateway"); got != "ghcr.io/mtsmfm/iwato-gateway:"+version || !pullsGatewayImage() || imageMoves() {
		t.Fatal(got)
	}
	defer func(d string) { imageDigests = d }(imageDigests)
	imageDigests = "iwato-gateway=sha256:aa,iwato-sandbox=sha256:bb"
	if got := imageRef("iwato-sandbox"); got != "ghcr.io/mtsmfm/iwato-sandbox:"+version+"@sha256:bb" {
		t.Fatal(got)
	}
	imageTag = "edge"
	if got := imageRef("iwato-gateway"); got != "ghcr.io/mtsmfm/iwato-gateway:edge@sha256:aa" || !pullsGatewayImage() || imageMoves() {
		t.Fatal(got)
	}
	imageDigests = ""
	if got := imageRef("iwato-sandbox"); got != "ghcr.io/mtsmfm/iwato-sandbox:edge" || pullsGatewayImage() || !imageMoves() {
		t.Fatal(got)
	}
}
