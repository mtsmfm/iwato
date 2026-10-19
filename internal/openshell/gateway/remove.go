package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"iwato/internal/openshell/wslc"
)

// Remove deletes the gateway's WSLc resources for a reset: the container
// (only one Iwato made, which carries labelImage), the state volume (the
// gateway's PKI, the providers' credentials and the policies), the network
// and the gateway's images (the container's and the one Ensure would use).
// A missing resource is fine. It goes on after a failure and returns what
// it removed, with the failures joined.
func Remove(ctx context.Context, run wslc.Runner, o Options) (removed []string, err error) {
	var errs []error
	images := []string{}
	if tag, terr := imageTag(o); o.Image != "" {
		images = append(images, o.Image)
	} else if terr == nil {
		images = append(images, tag)
	}
	var c struct {
		Labels map[string]string
		Config struct{ Labels map[string]string }
	}
	switch ierr := wslc.Inspect(ctx, run, "container", o.container(), &c); {
	case errors.Is(ierr, wslc.ErrNotFound):
	case ierr != nil:
		errs = append(errs, ierr)
	default:
		image := c.Config.Labels[labelImage]
		if image == "" {
			image = c.Labels[labelImage]
		}
		if image == "" {
			// Its volume and network are not Iwato's to remove either.
			return nil, fmt.Errorf("refusing container %s: it has no %s label (Iwato did not create it)", o.container(), labelImage)
		}
		if _, rerr := run.Run(ctx, nil, "container", "remove", "--force", o.container()); rerr != nil {
			errs = append(errs, rerr)
			break
		}
		removed = append(removed, "container "+o.container())
		if !slices.Contains(images, image) {
			images = append(images, image)
		}
	}
	for _, r := range []struct{ kind, name string }{{"volume", o.volume()}, {"network", o.Prefix}} {
		var v map[string]any
		switch ierr := wslc.Inspect(ctx, run, r.kind, r.name, &v); {
		case errors.Is(ierr, wslc.ErrNotFound):
		case ierr != nil:
			errs = append(errs, ierr)
		default:
			if _, rerr := run.Run(ctx, nil, r.kind, "remove", r.name); rerr != nil {
				errs = append(errs, rerr)
			} else {
				removed = append(removed, r.kind+" "+r.name)
			}
		}
	}
	for _, image := range images {
		var v map[string]any
		switch ierr := wslc.Inspect(ctx, run, "image", image, &v); {
		case errors.Is(ierr, wslc.ErrNotFound):
		case ierr != nil:
			errs = append(errs, ierr)
		default:
			if _, rerr := run.Run(ctx, nil, "image", "remove", image); rerr != nil {
				errs = append(errs, rerr)
			} else {
				removed = append(removed, "image "+image)
			}
		}
	}
	return removed, errors.Join(errs...)
}
