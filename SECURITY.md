# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| `main` | Yes |
| older releases | No |

We currently provide security fixes only for the latest code on `main`.

## Reporting a Vulnerability

Do not file public GitHub issues for security vulnerabilities.

MPC signing infrastructure is security-critical. A vulnerability here can result in loss of funds or exposure of key material, so we treat reports with high priority.

### How to report

Send an email to **security@brolabel.io** with:

- a short description of the issue
- steps to reproduce
- affected component or flow
- impact and severity assessment, if known
- your name or handle if you want credit

If you want to share encrypted details, mention that in the email and we can coordinate a secure follow-up channel.

## Response Timeline

| Stage | Target |
|-------|--------|
| Initial acknowledgement | 48 hours |
| Severity assessment | 5 business days |
| Fix or mitigation plan | 15 business days |
| Disclosure timing | Coordinated with the reporter |

We follow coordinated disclosure and ask for reasonable time to investigate and fix the issue before public disclosure.

## Scope

### In scope

- key generation and signing flow correctness
- unauthorized signing
- key share confidentiality
- share encryption and decryption
- key material remaining in memory longer than intended
- authentication or authorization bypass on co-signer interfaces
- vulnerabilities in cryptographic or signing dependencies

### Out of scope

- vulnerabilities in infrastructure outside this repository
- purely theoretical attacks with no practical exploit path
- denial of service issues unless they also expose key material
- documentation or test-only issues without security impact

## Threat Model

This project assumes:

- the signer side may be compromised
- the network is untrusted
- the co-signer host is a quorum-bearing trust domain and is **not protected**
  by v1: it holds B and C and can authorize threshold signing without A

Issues that expose the co-signer host, artifacts, or shared key as a recovery
quorum are critical. Offline recovery in v1 is only an artifact format and a
customer custody responsibility; it is not a supported recovery product.

## Local artifact key custody

The primary and recovery artifact stores are bound to distinct, non-overlapping
absolute directories and fixed party purposes. Both stores use one lifetime
AES-256 key supplied as canonical standard base64 plus a non-secret key
reference. The service rejects passphrases, non-canonical encodings, and keys
whose decoded length is not exactly 32 bytes; it never derives an encryption key
by hashing a passphrase.

Operators must preserve the original encryption key, key reference, primary
artifact directory, recovery artifact directory, and stable state/lock path.
Changing the key or key reference while artifacts remain live makes recovery
unsafe. There is no rotation, re-encryption, or old-key lookup in v1. A local
filesystem lock is process fencing for exactly one replica and one local
writable state filesystem shared by B/C; it is not cross-host or distributed
fencing. Upgrades require no overlap between old and new writable processes.

The co-signer host is a quorum-bearing trust domain. Compromise of that host,
or compromise of the shared key together with both B and C artifacts, can
authorize recovery signing without platform party A. This accepted v1 risk must
be treated as Critical. Customers retain custody and backup responsibility for
their artifacts, original key, and key reference. After activation the product
does not promise to monitor or guarantee C availability, and it provides no
supported recovery tool. A future tool is `FUTURE-001` in the approved DESIGN,
not a current product capability.

## Severity Classification

| Severity | Examples |
|----------|----------|
| Critical | Unilateral signing or private key reconstruction from one side |
| High | Key share leakage, auth bypass, failure to clear sensitive material |
| Medium | Insecure defaults, side-channel leakage with practical impact |
| Low | Minor weaknesses without a practical exploit path |

## Bug Bounty

We do not currently run a formal bug bounty program. We may offer public recognition or discretionary rewards for significant findings.

## Hall of Fame

Thank you to the researchers who disclose issues responsibly.
