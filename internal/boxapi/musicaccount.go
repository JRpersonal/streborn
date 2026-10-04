package boxapi

import "context"

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
