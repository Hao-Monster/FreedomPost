// String beacons are text/plain; browser JSON write routes require this MIME.
export function sendViewBeacon(slug: string, localId: string): void {
  navigator.sendBeacon?.(`/api/posts/${encodeURIComponent(slug)}/view`,
    new Blob([JSON.stringify({ localId })], { type: "application/json" }));
}
