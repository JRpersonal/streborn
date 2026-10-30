package dlna

import (
	"context"
	"fmt"
	"net/http"
)

// ItemMetadata returns one item by a BrowseMetadata call: its title and the
// stream URL the server hands out for it right now. A key that stored an
// item's URL asks this when the URL stopped serving, because some servers
// renumber their file URLs while the object id stays (#919: Windows media
// sharing builds them around ids it hands out anew on every restart).
func ItemMetadata(ctx context.Context, srv Server, objectID string) (Item, error) {
	if objectID == "" || objectID == "0" {
		return Item{}, fmt.Errorf("browse metadata: no item id")
	}
	raw, status, err := browseRaw(ctx, srv, objectID, browseMetadata, 0, 0)
	if err != nil {
		return Item{}, err
	}
	if status != http.StatusOK {
		return Item{}, fmt.Errorf("browse metadata failed: %s", soapFaultMessage(raw, status))
	}
	res, err := parseBrowseResponse(raw)
	if err != nil {
		return Item{}, err
	}
	for _, it := range res.Items {
		if it.ID == objectID || len(res.Items) == 1 {
			return it, nil
		}
	}
	return Item{}, fmt.Errorf("browse metadata: no item %q in the answer", objectID)
}
