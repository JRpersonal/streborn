#!/usr/bin/env bash
# Submit one artifact to Apple's notary service, wait for a verdict, staple it.
#
# Usage: notarize.sh <path-to-.app-or-.dmg>
# Needs: APPLE_KEY_FILE, APPLE_API_KEY_ID, APPLE_API_ISSUER_ID
#
# Why this is not just `notarytool submit --wait`:
#
# `--wait --timeout` gives up on the CLIENT side while the submission keeps
# going at Apple. The first real run did exactly that (v0.9.33, 2026-08-04):
# Apple took longer than thirty minutes on the first submission from a
# brand-new Developer ID, the step failed, and a submission that in all
# likelihood completed a few minutes later was thrown away along with the whole
# release. Waiting longer alone does not fix that, it just moves the cliff.
#
# So: submit without waiting, then poll for the verdict. A slow queue costs
# time, never the run. The only thing that ends it is a real verdict from
# Apple or the overall deadline below, and the deadline is generous because
# there is nothing to gain from failing early.
set -euo pipefail

ARTIFACT="${1:?usage: notarize.sh <path>}"
: "${APPLE_KEY_FILE:?APPLE_KEY_FILE is required}"
: "${APPLE_API_KEY_ID:?APPLE_API_KEY_ID is required}"
: "${APPLE_API_ISSUER_ID:?APPLE_API_ISSUER_ID is required}"

# Apple's own guidance is that most submissions finish within 15 minutes. This
# is the ceiling for the pathological case, not the expected wait.
DEADLINE_MIN="${NOTARY_DEADLINE_MIN:-30}"
POLL_SECONDS=30
# What to do when Apple has not answered by the deadline: "fail" stops the
# release, "continue" ships the build unnotarized and says so.
#
# Continue is the right default for this project. Apple took 27 to 28 HOURS
# over the first three submissions from a new Developer ID, all three of which
# were eventually Accepted, so the build was never the problem: waiting was.
# A release that is finished and green on every other platform must not sit
# behind that. An unnotarized Mac build still installs, it just shows the
# first-launch warning the website documents.
ON_TIMEOUT="${NOTARY_ON_TIMEOUT:-fail}"

auth=(--key "$APPLE_KEY_FILE" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER_ID")
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The notary service takes a zip, a UDIF disk image or a flat installer package.
# A bundle has to be zipped, and with ditto rather than zip: plain zip drops the
# symlinks and metadata a bundle needs and the service rejects the result.
upload="$ARTIFACT"
if [ -d "$ARTIFACT" ]; then
  upload="$work/upload.zip"
  ditto -c -k --keepParent "$ARTIFACT" "$upload"
fi

echo "Submitting $(basename "$ARTIFACT") to the notary service"
submit_json="$work/submit.json"
if ! xcrun notarytool submit "$upload" "${auth[@]}" --output-format json > "$submit_json"; then
  echo "::error::notarytool could not accept the submission at all. Check the credentials."
  cat "$submit_json" || true
  exit 1
fi
cat "$submit_json"

id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("id",""))' "$submit_json")"
if [ -z "$id" ]; then
  echo "::error::No submission id came back; nothing to wait for."
  exit 1
fi
echo "Submission id: $id"
# Printed on its own line so a failed run can be picked up by hand later: the
# submission survives at Apple even when this job does not.
echo "::notice::Notarization submission $id for $(basename "$ARTIFACT")"

deadline=$(( $(date +%s) + DEADLINE_MIN * 60 ))
status="In Progress"
while [ "$status" = "In Progress" ] || [ -z "$status" ]; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    if [ "$ON_TIMEOUT" = "continue" ]; then
      # NOT an error. The submission keeps going at Apple and will very likely
      # be accepted; it simply will not be part of this build. Recorded so the
      # publish guard can tell this apart from a rejection, which must block.
      echo "::warning::Apple has not returned a verdict for $id within ${DEADLINE_MIN} minutes. Shipping this build WITHOUT notarization. The submission is still queued at Apple; check it later with: xcrun notarytool info $id"
      [ -n "${GITHUB_OUTPUT:-}" ] && echo "notary_result=timeout" >> "$GITHUB_OUTPUT"
      exit 0
    fi
    echo "::error::Apple has not returned a verdict for $id within ${DEADLINE_MIN} minutes. The submission is still queued there; re-run the release, or check it later with: xcrun notarytool info $id"
    exit 1
  fi
  sleep "$POLL_SECONDS"
  info_json="$work/info.json"
  if ! xcrun notarytool info "$id" "${auth[@]}" --output-format json > "$info_json" 2>/dev/null; then
    # A transient API hiccup must not end the wait.
    echo "  (status query failed, retrying)"
    continue
  fi
  status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("status",""))' "$info_json" 2>/dev/null || echo "")"
  echo "  status: ${status:-unknown}"
done

# Always fetch the log, not only on a rejection: Apple returns warnings on
# accepted submissions too, and a step that only runs on the failure path is a
# step that has never been exercised when it is finally needed.
xcrun notarytool log "$id" "${auth[@]}" "$work/log.json" || true
cat "$work/log.json" || true

if [ "$status" != "Accepted" ]; then
  # A REJECTION always stops the release, whatever ON_TIMEOUT says. Apple
  # looked at the build and refused it, and shipping something Gatekeeper has
  # explicitly rejected would be worse than shipping something it has not seen.
  echo "::error::Notarization of $(basename "$ARTIFACT") ended as '$status'. The reason is in the log above."
  [ -n "${GITHUB_OUTPUT:-}" ] && echo "notary_result=rejected" >> "$GITHUB_OUTPUT"
  exit 1
fi

xcrun stapler staple "$ARTIFACT"
xcrun stapler validate "$ARTIFACT"
[ -n "${GITHUB_OUTPUT:-}" ] && echo "notary_result=accepted" >> "$GITHUB_OUTPUT"
echo "$(basename "$ARTIFACT"): notarized and stapled"
