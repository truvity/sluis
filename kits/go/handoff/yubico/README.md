# Yubico's attestation certificates

The root and the intermediate that a YubiKey's attestation chain ends in, as the card driver embeds them (`internal/card/certs` of
[github.com/truvity/atoom/key/yubikeypiv](https://github.com/truvity/atoom/tree/master/key/yubikeypiv)): the driver moved to atoom, and the
deploy script (`deploy/boards/handoff.sh`) and the tests of this kit read the certificates from here. `attest.go` holds the same root in
code, and its tests hold the two to the files.
