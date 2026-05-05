# Test Certificates

**WARNING: These are dummy certificates for testing purposes only.**

These certificates and keys are:
- Self-signed
- **NOT secure for production use**
- Only to be used in automated tests
- Safe to commit to public repositories

## Files

- `localhost-server.crt` - Test certificate for localhost TLS testing
- `localhost-server.fakekey` - Private key for the test certificate (dummy/test key)

## Regeneration

To regenerate these test certificates (if needed):

```bash
# Generate private key
openssl genrsa -out localhost-server.fakekey 2048

# Generate self-signed certificate
openssl req -new -x509 -key localhost-server.fakekey -out localhost-server.crt -days 3650 \
  -subj "/C=US/ST=TestState/L=TestCity/O=TEST CERTIFICATE - DO NOT USE IN PRODUCTION/OU=Testing/CN=localhost"
```

**Remember:** These are DUMMY certificates for testing only!
