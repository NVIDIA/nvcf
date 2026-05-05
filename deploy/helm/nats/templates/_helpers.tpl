{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/}}

{{- define "nvcf-nats.authCalloutSecretName" -}}
{{- $natsValues := default dict (get .Values "nats") -}}
{{- $authCallout := default dict (get $natsValues "authCallout") -}}
{{- default "nats-auth-callout-nkeys" $authCallout.secretName -}}
{{- end -}}

{{/*
NKey generation primitives. Shared between nkey-secret.yaml (single USER
NKey for the natsBox shared-worker) and nats-auth-callout-nkeys-secret.yaml
(USER + ACCOUNT NKey pair for the AUTH account). Mirrors the encoding
nkeys.CreatePair does in https://github.com/nats-io/nkeys/blob/main/keypair.go:
  prefix bytes  ||  ed25519 seed (32B)  or  ed25519 public key (32B)  ||  CRC16
  base32 encoded with `=` padding stripped.

Helm sprig has no bitwise ops so xor + CRC16 are simulated arithmetically.
*/}}

{{/*
nvcf-nats.intToChar — index a 256-byte lookup table to convert a byte value
(0..255) to its single-character representation. printf "%c" mishandles
some non-printable bytes, so we read from a base64-decoded byte table.
Input: integer 0..255. Output: 1-character string.
*/}}
{{- define "nvcf-nats.intToChar" -}}
  {{- $c := . -}}
  {{- $byteTab := list ("AA==" | b64dec) ("AQ==" | b64dec) ("Ag==" | b64dec) ("Aw==" | b64dec) ("BA==" | b64dec) ("BQ==" | b64dec) ("Bg==" | b64dec) ("Bw==" | b64dec) ("CA==" | b64dec) ("CQ==" | b64dec) ("Cg==" | b64dec) ("Cw==" | b64dec) ("DA==" | b64dec) ("DQ==" | b64dec) ("Dg==" | b64dec) ("Dw==" | b64dec) ("EA==" | b64dec) ("EQ==" | b64dec) ("Eg==" | b64dec) ("Ew==" | b64dec) ("FA==" | b64dec) ("FQ==" | b64dec) ("Fg==" | b64dec) ("Fw==" | b64dec) ("GA==" | b64dec) ("GQ==" | b64dec) ("Gg==" | b64dec) ("Gw==" | b64dec) ("HA==" | b64dec) ("HQ==" | b64dec) ("Hg==" | b64dec) ("Hw==" | b64dec) ("IA==" | b64dec) ("IQ==" | b64dec) ("Ig==" | b64dec) ("Iw==" | b64dec) ("JA==" | b64dec) ("JQ==" | b64dec) ("Jg==" | b64dec) ("Jw==" | b64dec) ("KA==" | b64dec) ("KQ==" | b64dec) ("Kg==" | b64dec) ("Kw==" | b64dec) ("LA==" | b64dec) ("LQ==" | b64dec) ("Lg==" | b64dec) ("Lw==" | b64dec) ("MA==" | b64dec) ("MQ==" | b64dec) ("Mg==" | b64dec) ("Mw==" | b64dec) ("NA==" | b64dec) ("NQ==" | b64dec) ("Ng==" | b64dec) ("Nw==" | b64dec) ("OA==" | b64dec) ("OQ==" | b64dec) ("Og==" | b64dec) ("Ow==" | b64dec) ("PA==" | b64dec) ("PQ==" | b64dec) ("Pg==" | b64dec) ("Pw==" | b64dec) ("QA==" | b64dec) ("QQ==" | b64dec) ("Qg==" | b64dec) ("Qw==" | b64dec) ("RA==" | b64dec) ("RQ==" | b64dec) ("Rg==" | b64dec) ("Rw==" | b64dec) ("SA==" | b64dec) ("SQ==" | b64dec) ("Sg==" | b64dec) ("Sw==" | b64dec) ("TA==" | b64dec) ("TQ==" | b64dec) ("Tg==" | b64dec) ("Tw==" | b64dec) ("UA==" | b64dec) ("UQ==" | b64dec) ("Ug==" | b64dec) ("Uw==" | b64dec) ("VA==" | b64dec) ("VQ==" | b64dec) ("Vg==" | b64dec) ("Vw==" | b64dec) ("WA==" | b64dec) ("WQ==" | b64dec) ("Wg==" | b64dec) ("Ww==" | b64dec) ("XA==" | b64dec) ("XQ==" | b64dec) ("Xg==" | b64dec) ("Xw==" | b64dec) ("YA==" | b64dec) ("YQ==" | b64dec) ("Yg==" | b64dec) ("Yw==" | b64dec) ("ZA==" | b64dec) ("ZQ==" | b64dec) ("Zg==" | b64dec) ("Zw==" | b64dec) ("aA==" | b64dec) ("aQ==" | b64dec) ("ag==" | b64dec) ("aw==" | b64dec) ("bA==" | b64dec) ("bQ==" | b64dec) ("bg==" | b64dec) ("bw==" | b64dec) ("cA==" | b64dec) ("cQ==" | b64dec) ("cg==" | b64dec) ("cw==" | b64dec) ("dA==" | b64dec) ("dQ==" | b64dec) ("dg==" | b64dec) ("dw==" | b64dec) ("eA==" | b64dec) ("eQ==" | b64dec) ("eg==" | b64dec) ("ew==" | b64dec) ("fA==" | b64dec) ("fQ==" | b64dec) ("fg==" | b64dec) ("fw==" | b64dec) ("gA==" | b64dec) ("gQ==" | b64dec) ("gg==" | b64dec) ("gw==" | b64dec) ("hA==" | b64dec) ("hQ==" | b64dec) ("hg==" | b64dec) ("hw==" | b64dec) ("iA==" | b64dec) ("iQ==" | b64dec) ("ig==" | b64dec) ("iw==" | b64dec) ("jA==" | b64dec) ("jQ==" | b64dec) ("jg==" | b64dec) ("jw==" | b64dec) ("kA==" | b64dec) ("kQ==" | b64dec) ("kg==" | b64dec) ("kw==" | b64dec) ("lA==" | b64dec) ("lQ==" | b64dec) ("lg==" | b64dec) ("lw==" | b64dec) ("mA==" | b64dec) ("mQ==" | b64dec) ("mg==" | b64dec) ("mw==" | b64dec) ("nA==" | b64dec) ("nQ==" | b64dec) ("ng==" | b64dec) ("nw==" | b64dec) ("oA==" | b64dec) ("oQ==" | b64dec) ("og==" | b64dec) ("ow==" | b64dec) ("pA==" | b64dec) ("pQ==" | b64dec) ("pg==" | b64dec) ("pw==" | b64dec) ("qA==" | b64dec) ("qQ==" | b64dec) ("qg==" | b64dec) ("qw==" | b64dec) ("rA==" | b64dec) ("rQ==" | b64dec) ("rg==" | b64dec) ("rw==" | b64dec) ("sA==" | b64dec) ("sQ==" | b64dec) ("sg==" | b64dec) ("sw==" | b64dec) ("tA==" | b64dec) ("tQ==" | b64dec) ("tg==" | b64dec) ("tw==" | b64dec) ("uA==" | b64dec) ("uQ==" | b64dec) ("ug==" | b64dec) ("uw==" | b64dec) ("vA==" | b64dec) ("vQ==" | b64dec) ("vg==" | b64dec) ("vw==" | b64dec) ("wA==" | b64dec) ("wQ==" | b64dec) ("wg==" | b64dec) ("ww==" | b64dec) ("xA==" | b64dec) ("xQ==" | b64dec) ("xg==" | b64dec) ("xw==" | b64dec) ("yA==" | b64dec) ("yQ==" | b64dec) ("yg==" | b64dec) ("yw==" | b64dec) ("zA==" | b64dec) ("zQ==" | b64dec) ("zg==" | b64dec) ("zw==" | b64dec) ("0A==" | b64dec) ("0Q==" | b64dec) ("0g==" | b64dec) ("0w==" | b64dec) ("1A==" | b64dec) ("1Q==" | b64dec) ("1g==" | b64dec) ("1w==" | b64dec) ("2A==" | b64dec) ("2Q==" | b64dec) ("2g==" | b64dec) ("2w==" | b64dec) ("3A==" | b64dec) ("3Q==" | b64dec) ("3g==" | b64dec) ("3w==" | b64dec) ("4A==" | b64dec) ("4Q==" | b64dec) ("4g==" | b64dec) ("4w==" | b64dec) ("5A==" | b64dec) ("5Q==" | b64dec) ("5g==" | b64dec) ("5w==" | b64dec) ("6A==" | b64dec) ("6Q==" | b64dec) ("6g==" | b64dec) ("6w==" | b64dec) ("7A==" | b64dec) ("7Q==" | b64dec) ("7g==" | b64dec) ("7w==" | b64dec) ("8A==" | b64dec) ("8Q==" | b64dec) ("8g==" | b64dec) ("8w==" | b64dec) ("9A==" | b64dec) ("9Q==" | b64dec) ("9g==" | b64dec) ("9w==" | b64dec) ("+A==" | b64dec) ("+Q==" | b64dec) ("+g==" | b64dec) ("+w==" | b64dec) ("/A==" | b64dec) ("/Q==" | b64dec) ("/g==" | b64dec) ("/w==" | b64dec) -}}
  {{- index $byteTab $c -}}
{{- end -}}

{{/*
nvcf-nats.xor — bitwise XOR over up to 16 bits, simulated arithmetically.
Sprig exposes no bitwise ops; we walk each bit position with mod/div and
sum power-of-two contributions where the input bits differ.
Inputs (dict): a, b — non-negative integers fitting in 16 bits.
Output: integer (a XOR b).
*/}}
{{- define "nvcf-nats.xor" -}}
  {{- $a := .a -}}
  {{- $b := .b -}}
  {{- $result := 0 -}}
  {{- $power := 1 -}}
  {{- range $i := until 16 -}}
    {{- $bitA := mod (div $a $power) 2 -}}
    {{- $bitB := mod (div $b $power) 2 -}}
    {{- if ne $bitA $bitB -}}
      {{- $result = add $result $power -}}
    {{- end -}}
    {{- $power = mul $power 2 -}}
  {{- end -}}
  {{- $result -}}
{{- end -}}

{{/*
nvcf-nats.crc16 — NATS NKey CRC16-CCITT(0x1021) implementation, mirrors
https://github.com/nats-io/nkeys/blob/main/crc16.go. Returns the 16-bit
CRC packed little-endian as a 2-byte string suitable for appending to
the seed/public-key payload before base32 encoding.
Input: byte string. Output: 2-character string (low byte || high byte).
*/}}
{{- define "nvcf-nats.crc16" -}}
  {{- $payload := . -}}
  {{- $crc := 0 -}}
  {{- $crc16tab := list 0x0000 0x1021 0x2042 0x3063 0x4084 0x50a5 0x60c6 0x70e7 0x8108 0x9129 0xa14a 0xb16b 0xc18c 0xd1ad 0xe1ce 0xf1ef 0x1231 0x0210 0x3273 0x2252 0x52b5 0x4294 0x72f7 0x62d6 0x9339 0x8318 0xb37b 0xa35a 0xd3bd 0xc39c 0xf3ff 0xe3de 0x2462 0x3443 0x0420 0x1401 0x64e6 0x74c7 0x44a4 0x5485 0xa56a 0xb54b 0x8528 0x9509 0xe5ee 0xf5cf 0xc5ac 0xd58d 0x3653 0x2672 0x1611 0x0630 0x76d7 0x66f6 0x5695 0x46b4 0xb75b 0xa77a 0x9719 0x8738 0xf7df 0xe7fe 0xd79d 0xc7bc 0x48c4 0x58e5 0x6886 0x78a7 0x0840 0x1861 0x2802 0x3823 0xc9cc 0xd9ed 0xe98e 0xf9af 0x8948 0x9969 0xa90a 0xb92b 0x5af5 0x4ad4 0x7ab7 0x6a96 0x1a71 0x0a50 0x3a33 0x2a12 0xdbfd 0xcbdc 0xfbbf 0xeb9e 0x9b79 0x8b58 0xbb3b 0xab1a 0x6ca6 0x7c87 0x4ce4 0x5cc5 0x2c22 0x3c03 0x0c60 0x1c41 0xedae 0xfd8f 0xcdec 0xddcd 0xad2a 0xbd0b 0x8d68 0x9d49 0x7e97 0x6eb6 0x5ed5 0x4ef4 0x3e13 0x2e32 0x1e51 0x0e70 0xff9f 0xefbe 0xdfdd 0xcffc 0xbf1b 0xaf3a 0x9f59 0x8f78 0x9188 0x81a9 0xb1ca 0xa1eb 0xd10c 0xc12d 0xf14e 0xe16f 0x1080 0x00a1 0x30c2 0x20e3 0x5004 0x4025 0x7046 0x6067 0x83b9 0x9398 0xa3fb 0xb3da 0xc33d 0xd31c 0xe37f 0xf35e 0x02b1 0x1290 0x22f3 0x32d2 0x4235 0x5214 0x6277 0x7256 0xb5ea 0xa5cb 0x95a8 0x8589 0xf56e 0xe54f 0xd52c 0xc50d 0x34e2 0x24c3 0x14a0 0x0481 0x7466 0x6447 0x5424 0x4405 0xa7db 0xb7fa 0x8799 0x97b8 0xe75f 0xf77e 0xc71d 0xd73c 0x26d3 0x36f2 0x0691 0x16b0 0x6657 0x7676 0x4615 0x5634 0xd94c 0xc96d 0xf90e 0xe92f 0x99c8 0x89e9 0xb98a 0xa9ab 0x5844 0x4865 0x7806 0x6827 0x18c0 0x08e1 0x3882 0x28a3 0xcb7d 0xdb5c 0xeb3f 0xfb1e 0x8bf9 0x9bd8 0xabbb 0xbb9a 0x4a75 0x5a54 0x6a37 0x7a16 0x0af1 0x1ad0 0x2ab3 0x3a92 0xfd2e 0xed0f 0xdd6c 0xcd4d 0xbdaa 0xad8b 0x9de8 0x8dc9 0x7c26 0x6c07 0x5c64 0x4c45 0x3ca2 0x2c83 0x1ce0 0x0cc1 0xef1f 0xff3e 0xcf5d 0xdf7c 0xaf9b 0xbfba 0x8fd9 0x9ff8 0x6e17 0x7e36 0x4e55 0x5e74 0x2e93 0x3eb2 0x0ed1 0x1ef0 -}}
  {{- range $i := until ($payload | len) -}}
    {{- $byte := index $payload $i -}}
    {{- $tableIndex := include "nvcf-nats.xor" (dict "a" (div $crc 256) "b" $byte) | int -}}
    {{- $tableIndex = mod $tableIndex 256 -}}
    {{- $right := index $crc16tab $tableIndex -}}
    {{- $left := mod (mul $crc 256) 65536 -}}
    {{- $crc = include "nvcf-nats.xor" (dict "a" $left "b" $right) | int -}}
  {{- end -}}
  {{- $crcLow := mod $crc 256 -}}
  {{- $crcHigh := div $crc 256 -}}
  {{- $crcBytes := print (include "nvcf-nats.intToChar" $crcLow) (include "nvcf-nats.intToChar" $crcHigh) -}}
  {{ $crcBytes }}
{{- end -}}

{{/*
nvcf-nats.genNkeyPair — generate one NKey pair (seed + public key) and
return a `seed=...;pub=...` string the caller splits. The encoding follows
nkeys.CreatePair: ed25519 keypair, then base32(prefix || raw || crc16).

Inputs (dict):
  publicPrefixByte  integer — NATS NKey type prefix shifted left 3 bits.
                    USER    = 20 << 3 = 160 ('U...' public, 'S' + 'U' = 'SU' seed)
                    ACCOUNT =  0 << 3 =   0 ('A...' public, 'S' + 'A' = 'SA' seed)
                    See nkeys/strkey.go for the full set.
*/}}
{{- define "nvcf-nats.genNkeyPair" -}}
  {{- $publicPrefixByte := .publicPrefixByte -}}
  {{- $prefixByteSeed := mul 18 8 -}}
  {{- $b1 := add $prefixByteSeed (div $publicPrefixByte 32) -}}
  {{- $b2 := mul (mod $publicPrefixByte 32) 8 -}}
  {{- $b64Key := genPrivateKey "ed25519" -}}
  {{- $rawKey := $b64Key | replace "-----BEGIN PRIVATE KEY-----\n" "" | replace "\n-----END PRIVATE KEY-----" "" | b64dec -}}
  {{- $seed32 := substr 16 48 $rawKey -}}
  {{- $prefixBytes := print (include "nvcf-nats.intToChar" $b1) (include "nvcf-nats.intToChar" $b2) -}}
  {{- $seedPayload := print $prefixBytes $seed32 -}}
  {{- $seedCrc := include "nvcf-nats.crc16" $seedPayload -}}
  {{- $nkeySeed := (print $seedPayload $seedCrc) | b32enc | trimAll "=" -}}
  {{- $pubCert := genSelfSignedCertWithKey "" (list) (list) 0 $b64Key -}}
  {{- $pubCertPem := $pubCert.Cert | replace "-----BEGIN CERTIFICATE-----\n" "" | replace "\n-----END CERTIFICATE-----\n" "" | replace "\n" "" -}}
  {{- $pubCertBytes := $pubCertPem | b64dec -}}
  {{- $oneDateLen := "XXyymmddhhmmssZ" | len -}}
  {{- $fullPatternLen := mul 2 $oneDateLen | int -}}
  {{- $dateStart := -1 -}}
  {{- range $i := until (sub ($pubCertBytes | len) $fullPatternLen | int) -}}
    {{- $firstDate := substr $i (add $i $oneDateLen | int) $pubCertBytes -}}
    {{- $secondDateStart := add $i $oneDateLen | int -}}
    {{- $secondDate := substr $secondDateStart (add $secondDateStart $oneDateLen | int) $pubCertBytes -}}
    {{- if eq $firstDate $secondDate -}}
      {{- $dateStart = $i -}}
      {{- break -}}
    {{- end -}}
  {{- end -}}
  {{- $zeroSearchStart := add 2 (add $dateStart $fullPatternLen) | int -}}
  {{- $indexOfFirstZero := -1 -}}
  {{- range $i := untilStep $zeroSearchStart ($pubCertBytes | len) 1 -}}
    {{- $byte := index $pubCertBytes $i -}}
    {{- if eq $byte 0 -}}
      {{- $indexOfFirstZero = $i -}}
      {{- break -}}
    {{- end -}}
  {{- end -}}
  {{- $pubKeyStart := (add1 $indexOfFirstZero) | int -}}
  {{- $pubKeyBytes := substr $pubKeyStart ((add $pubKeyStart 32) | int) $pubCertBytes -}}
  {{- $pubKeyPrefix := include "nvcf-nats.intToChar" $publicPrefixByte -}}
  {{- $pubKeyPayload := print $pubKeyPrefix $pubKeyBytes -}}
  {{- $pubKeyPayloadCrc := include "nvcf-nats.crc16" $pubKeyPayload -}}
  {{- $pubKeyPayloadWithCrc := print $pubKeyPayload $pubKeyPayloadCrc -}}
  {{- $pubNkey := $pubKeyPayloadWithCrc | b32enc | trimAll "=" -}}
  {{- printf "seed=%s;pub=%s" $nkeySeed $pubNkey -}}
{{- end -}}
