package marge

// The BMX service registry in the firmware's OWN schema.
//
// STR used to answer GET /bmx/registry/v1/services with a hand-simplified
// shape: a "services" root holding flat {name,url,version} entries. The
// firmware ignores that silently, which is why declaring LOCAL_INTERNET_RADIO
// there had no effect no matter what the entry said. The real document uses a
// "bmx_services" root and per-service _links, assets, a numeric id{name,value},
// baseUrl (lower-case url) and streamTypes.
//
// Two placeholders are substituted per request with this agent's own base, the
// way the real cloud pointed at its adapters: {BMX_SERVER} and {MEDIA_SERVER}.
//
// authenticationModel.anonymousAccount{autoCreate,enabled} is the load-bearing
// part for STR: it makes the firmware create the account for that source
// ITSELF, with no account callback and no login. A ContentItem on such a
// source therefore cannot fail the not-logged-in check that breaks UPNP
// presets (the 1036 family).

const bmxServicesJSON = `{
  "_links": {
    "bmx_services_availability": {
      "href": "../servicesAvailability"
    }
  },
  "askAgainAfter": 1230482,
  "bmx_services": [
    {
      "_links": {
        "bmx_navigate": { "href": "/v1/navigate" },
        "bmx_token": { "href": "/v1/token" },
        "self": { "href": "/" }
      },
      "askAdapter": false,
      "assets": {
        "color": "#000000",
        "description": "Internet radio on SoundTouch.",
        "icons": {
          "defaultAlbumArt": "{MEDIA_SERVER}/bmx-icons/tunein/default-album-art.png",
          "largeSvg": "{MEDIA_SERVER}/bmx-icons/tunein/smallSvg.svg",
          "monochromePng": "{MEDIA_SERVER}/bmx-icons/tunein/monochromePng.png",
          "monochromeSvg": "{MEDIA_SERVER}/bmx-icons/tunein/monochromeSvg.svg",
          "smallSvg": "{MEDIA_SERVER}/bmx-icons/tunein/smallSvg.svg"
        },
        "name": "TuneIn"
      },
      "authenticationModel": {
        "anonymousAccount": { "autoCreate": true, "enabled": true }
      },
      "baseUrl": "{BMX_SERVER}/bmx/tunein",
      "id": { "name": "TUNEIN", "value": 25 },
      "streamTypes": [ "liveRadio", "onDemand" ]
    },
    {
      "_links": {
        "bmx_token": { "href": "/token" },
        "self": { "href": "/" }
      },
      "askAdapter": false,
      "assets": {
        "color": "#000000",
        "description": "Custom radio stations.",
        "icons": {
          "largeSvg": "{MEDIA_SERVER}/bmx-icons/orion/monochrome.svg",
          "monochromePng": "{MEDIA_SERVER}/bmx-icons/orion/monochrome_v2.png",
          "monochromeSvg": "{MEDIA_SERVER}/bmx-icons/orion/monochrome.svg",
          "smallSvg": "{MEDIA_SERVER}/bmx-icons/orion/monochrome.svg"
        },
        "name": "Custom Stations"
      },
      "authenticationModel": {
        "anonymousAccount": { "autoCreate": true, "enabled": true }
      },
      "baseUrl": "{BMX_SERVER}/core02/svc-bmx-adapter-orion/prod/orion",
      "id": { "name": "LOCAL_INTERNET_RADIO", "value": 11 },
      "streamTypes": [ "liveRadio" ]
    }
  ]
}`
