package boxapi

import (
	"context"
	"errors"
)

// musicServiceCredentialsBody is the firmware's own credentials document, the
// shape /setMusicServiceAccount takes for a linked service.
func musicServiceCredentialsBody(source, displayName, user, pass string) string {
	return `<credentials source="` + xmlEscape(source) +
		`" displayName="` + xmlEscape(displayName) + `"><user>` +
		xmlEscape(user) + `</user><pass>` + xmlEscape(pass) + `</pass></credentials>`
}

// SetMusicServiceAccount adds a music-service account through the speaker's
// local API, e.g. PANDORA: the firmware's own client logs in with it and then
// registers the source with its cloud (STR's stand-in).
func (c *Client) SetMusicServiceAccount(ctx context.Context, source, displayName, user, pass string) error {
	return c.postXML(ctx, "/setMusicServiceAccount", musicServiceCredentialsBody(source, displayName, user, pass))
}

// RemoveMusicServiceAccount removes such an account again (empty password).
func (c *Client) RemoveMusicServiceAccount(ctx context.Context, source, displayName, user string) error {
	return c.postXML(ctx, "/removeMusicServiceAccount", musicServiceCredentialsBody(source, displayName, user, ""))
}

// NotifySourcesUpdated tells the firmware its source list changed in the
// cloud (<updates deviceID=".."><sourcesUpdated/></updates> to :8090
// /notification), the message Bose's cloud sent after an account change. The
// firmware then reads the account document (/full) again and creates the
// accounts it names. The device id is the speaker's own, read from /info.
func (c *Client) NotifySourcesUpdated(ctx context.Context) error {
	info, err := c.GetInfo(ctx)
	if err != nil {
		return err
	}
	if info.DeviceID == "" {
		return errors.New("the speaker reported no device id")
	}
	return c.postXML(ctx, "/notification", `<updates deviceID="`+xmlEscape(info.DeviceID)+`"><sourcesUpdated/></updates>`)
}
