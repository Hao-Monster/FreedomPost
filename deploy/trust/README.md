# Public trust anchor

`cloudflare-origin-ca-rsa.pem` is the public Cloudflare Origin RSA CA root, downloaded from https://developers.cloudflare.com/ssl/static/origin_ca_rsa_root.pem on 2026-09-25. Source documentation: https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/ . It is not the server certificate or a private key.

SHA256 of the downloaded file: `91a8a5567efa6bf941162aa806b3ba476aaddf7867640e53053b35fb225a5dae`.

The production origin currently uses this RSA issuer. A future issuer change must update the trust anchor explicitly and verify the complete chain before releasing.
