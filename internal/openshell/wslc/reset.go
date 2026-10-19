package wslc

import (
	"context"
	"errors"
	"strings"
)

// RemoveAll removes every container and volume the driver made for
// namespace (its labels), whatever its role: a reset's sweep once the
// gateway is gone, for what a delete through OpenShell left (an interrupted
// delete, a gateway that could not be reached). It reports how many it
// removed, with the failures joined.
func RemoveAll(ctx context.Context, r Runner, namespace string) (containers, volumes int, err error) {
	w := wslc{r}
	var errs []error
	ids, lerr := w.list(ctx, map[string]string{labelManagedBy: "openshell", labelNamespace: namespace})
	if lerr != nil {
		errs = append(errs, lerr)
	}
	for _, id := range ids {
		if rerr := w.remove(ctx, id); rerr != nil {
			errs = append(errs, rerr)
		} else {
			containers++
		}
	}
	out, lerr := r.Run(ctx, nil, "volume", "list", "--quiet", "--filter", "label="+labelManagedBy+"=openshell", "--filter", "label="+labelNamespace+"="+namespace)
	if lerr != nil {
		errs = append(errs, lerr)
	}
	for _, name := range strings.Fields(string(out)) {
		if rerr := w.volumeRemove(ctx, name); rerr != nil {
			errs = append(errs, rerr)
		} else {
			volumes++
		}
	}
	return containers, volumes, errors.Join(errs...)
}
