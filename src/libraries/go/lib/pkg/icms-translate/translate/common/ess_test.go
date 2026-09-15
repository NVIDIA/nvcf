/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

const (
	jwtNotParseable   = "blahblah.eyJpc3MiOiJodHRwczovL25vdGFyeS1zZXJ2aWNlLnN0Zy5udmNmLm52aWRpYS5jb20iLCJzdWIiOiJudnNzYS1zdGctYWJjMTIzIiwiYXVkIjoiN2pleHVuc2EwZWNnemZvZ3NsbDZhc2hyOGlkb2EzMXllaHEwZnRpZ2QyYyIsImFzc2VydGlvbiI6eyJuYW1lc3BhY2UiOiJudmN0Iiwic2VjcmV0UGF0aHMiOlsidGFza3MvMTNlMmI1OTktOTZjYS00MmI1LWE0MTktOGZhN2Y3MDFkNWQyL3NlY3JldHMiXX0sImlhdCI6MTcyODQxMzA4NywianRpIjoiOGQyNzUwZTEtZjRhZS00YzMyLTlkNTYtZDFjY2U0YmU2OWZjIn0=.blahblah"
	jwtNoAssertion    = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMn0.NHVaYe26MbtOYhSKkoKYdFVomg4i8ZJd8_-RU8VNbftc4TSMb4bXP3l3YlNWACwyXPGffz5aXHc6lty1Y2t4SWRqGteragsVdZufDn5BlnJl9pdR_kdVFUsra2rWKEofkZeIC4yWytE58sMIihvo9H1ScmmVwBcQP6XETqYd0aSHp1gOa9RdUPDvoXQ5oqygTqVtxaDr6wUFKrKItgBMzWIdNZ6y7O9E0DhEPTbE9rfBo6KTFsHAZnMg4k68CDp2woYIaXbmYTWcvbzIuHO7_37GT79XdIwkm95QJ7hYC9RiwrV7mesbY4PAahERJawntho0my942XheVLmGwLMBkQ"
	jwtAssertionStr   = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjoic3RyaW5ndmFsIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMn0.cNzsz41TjDrdxO2RBcGk8VirgnijFGQqzfHod_U40_UXzNLbmbBuPnX6n1kn6-Hcy3lgFg8w8301ea1sUkn2FxzrN3Eod2OA5ufGpEQ2wLtD0e7Nb44azq590GE2U5TQCBnX2par_IDfWMazmjhm4QC_hqrtPDbsEd339OTNz05KwYTD6DZe0Pnk8C5JHKjiv9WWiAZKMqEJa0sWS3KJxQUtQkYBl3tFcRRaHS5Lp0uiTdKDPTLdOcuMcfINJ1yQA-0NgNXshIIzyoX6R-q1aJvlZdaSqFdUj4yUAniOgKRhwmA1aWfoOaefs4ZTd4kMStX_bcvtW3mkiOdPmDvj-Q"
	jwtNoSecretPaths  = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7fSwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMn0.jgWZxwgWnIXpEgjecsN11Y5sflvsBtv9XyuKFcsr2h6MwmfHmKkLp1OKRCp8R4RkpIzaeFTzop696gbVN5YgG4F-7HVQIb-XDaIhO-yLy3vJ7brugFHx3T6gqAzKDbP4C6M44_S3ZCR6CGvxkWMaMKxJv4Ne3aCw-Kff1YZtp8Ecmo847j0gAiE0Wfh7yse4UYrf-g43_XAkA_t7JtyKjpyZSosD3WgWLBCur3HSFkW6KtdXSviQPnbB7_dOL5D8O-KOnF0hfdsBbp7Q5Ez5XqQj5vcCTMSQIVfM1UHpPQIBA4F3dY7U13Y9krn-DWh7R1jtInj4JFUuAIMlp2zQuw"
	jwtSecretPathsStr = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjoiYWNjb3VudHMve25jYS1pZH0vdGVsZW1ldHJ5L3t0ZWxlbWV0cnktaWR9In0sImFkbWluIjp0cnVlLCJpYXQiOjE1MTYyMzkwMjJ9.sX4EW5JU_zVfcporksBMPFzviiZlUjQOWLeIMmFeEK7KJ2Dj71m_smznnQTJlMg-FPQgAsYrIjj4M6wnKNQLWiLrNEutS5rqfvnDoq_W-ctv3WseorwBp1-H0_cqbdYaBA6rM-elNmO8C5LQLvq31S2K4ynhozzPkfwrtuRMyRVS1FmQGgXVMkusiMgPzbx65NMkOyUYRUOrwK_DjcShYdZ-Yz6tEj5fCoTD_TJVC2go77eK1zo4wz8NwJLNSnOkaINqvIe4MxaWeUXaoSuPsvCNg-b5woJ-V1PT-fw7-jPAKxzvotrNhTGic5sekQKh6m-WbwmeJSVYLZOUY6QwXA"
	jwtEmptyPaths     = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjpbXX0sImFkbWluIjp0cnVlLCJpYXQiOjE1MTYyMzkwMjJ9.YiNPFtF6OfSJgBKiuIT1QFGe_VvbZCuO2ZxADa_NABznj5pwMj-hxx77GBmfbQ5x6c7KeyMxdwP_rI9QjmXV8kpBN4iTsVouoHq62RvsKkp8DIQD_3gXI-dfOuD1thO3uLaQD4OTxUlf4aSvydAPULeUGh10UwdHcPH_HlqW7RjQSl8NM3waZNBZEy2GpZt1d4tAwJyN8s4gMmSWcU0KKVWgTPuJHasIR9meuCtZIhxarA0ZmjVXROOejze_9ztyPvibVaW1MySStBAE3IkbGk19JgS3ZvBD0vWq3R6UFYok4-nVBuFe_fNFCm3sRUdxvhwHL4ogH8yK6DRICwv88w"
	jwtNewAcctPaths   = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjpbImFjY291bnRzL19sSUxYQi0xTmZObUJuUVNrX3NwcVZXT3RDQVhRbTUwVUVNd2ozVFJneW1KSjJBeXV3Y2d4cS90ZWxlbWV0cnkvNmZhODM2NmUtN2JhMi00MTQzLWE3NDQtNzUxNWZhZjliZjcyIiwiYWNjb3VudHMvX2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxL3RlbGVtZXRyeS82ZmE4MzY2ZS03YmEyLTQxNDMtYTc0NC03NTE1ZmFmOWJmODEiXX0sImFkbWluIjp0cnVlLCJpYXQiOjE1MTYyMzkwMjJ9.SpQYbFe1nfrQ5KshRly9SUC26W_j2pQh6DMinsbrsQHvKg1se2oH3VzoinbMbQz_5LXcg-XNkx4cNJN2AjuwUIzk6DIULICHequjq-xAagFR8_z25o11d01zBS5NxF9ACgtIl69dhTHk8sK2eQb4AFGCFff61j0kXabIYESGJxdv9RkNfWtYZ-FmIc9uF4jY59zR1EBdXilcccR0RiCvKAlTYorE7Tj-04KgTFnvQbmP0TQGQd6xbqdAaPRBpXyBG04qmA296TfrAOV_02aIdtjHa5SjmvPAbVeHVV5Bx_Zd8yVmyN0e7qegnAOqc5NP3kF38W4nWhTE8VkNTZjTPA"
	jwtOldAcctPath    = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjpbImFjY291bnRzL19sSUxYQi0xTmZObUJuUVNrX3NwcVZXT3RDQVhRbTUwVUVNd2ozVFJneW1KSjJBeXV3Y2d4cS9zZWNyZXRzIiwidGFza3MvMTM2YzJjZjAtNDhmMS00ZTQ1LTgwMGEtODJlODVkNWYyNjVjL3NlY3JldHMiXX0sImFkbWluIjp0cnVlLCJpYXQiOjE1MTYyMzkwMjJ9.XXLn1ABcKgcu1IDl2xkQdfkhxl2PHS7SPclmBLkjJY2YaSKi1DHnTmfgC1LJJtq-7HJw7Z5EJS5-z5hnmzzZx99g2amnQtTN4xHksoJPp31Jcx1X6z2aGesjkSLHlKMT5whIbkVB3T2oY-uVXTMPG-ruyMQ87P17_jqaASvcxnbhJBqBT95DlDWgin5j2qqA-XHgfIKnel_OxObo9U1jV10C3tR49pnmjqerzjVdP_vUhR3C9Hx9TE6fTFEO3W1j7xBkd4klaj2KMjSzR1bRwZ6eiWDFPgCs0CYHcmq14DY3kNY8dGzwB9XkOk4JRAkFNTZ7HIuXPfZPjR7uFXdVMw"
	jwtTaskPath       = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjpbInRhc2tzLzEzNmMyY2YwLTQ4ZjEtNGU0NS04MDBhLTgyZTg1ZDVmMjY1Yy9zZWNyZXRzIl19LCJhZG1pbiI6dHJ1ZSwiaWF0IjoxNTE2MjM5MDIyfQ.b-EUN0UmmqUfp2TKY5DkNn7vgjnDkcnEHHMvrw4ECyskfiyOEyCNzdvyiHdt-lcudUfQ81fs7P6cmfNwPvGdNkfcnqEST3cttQUNvweYG_YlbHe7ciovxX2L-SDLdSW1hJ3WjR_glutgTami71Eq3AMeYc-p2RRH5_IvJ4j6yaJDmAZJJJqef_1WPN_v4xUiYWkFaE14ZefauEz8XnqSqUnZdaZZvrP9LLrOM8uSV1tfGB8dwLa4XT7_kxEnuU_WaAU7P9KleqwfFEIC7PJU4HMmFVFThyIs6KkLtk_SlDtL49XWCSlalUgg7OSCqG17NPx0wG6QeGrp4rKQvm-8dA"
	jwtAllPaths       = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjpbImFjY291bnRzL19sSUxYQi0xTmZObUJuUVNrX3NwcVZXT3RDQVhRbTUwVUVNd2ozVFJneW1KSjJBeXV3Y2d4cS90ZWxlbWV0cnkvNmZhODM2NmUtN2JhMi00MTQzLWE3NDQtNzUxNWZhZjliZjcyIiwiYWNjb3VudHMvX2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxL3RlbGVtZXRyeS82ZmE4MzY2ZS03YmEyLTQxNDMtYTc0NC03NTE1ZmFmOWJmODEiLCJhY2NvdW50cy9fbElMWEItMU5mTm1CblFTa19zcHFWV090Q0FYUW01MFVFTXdqM1RSZ3ltSkoyQXl1d2NneHEvc2VjcmV0cyIsInRhc2tzLzEzNmMyY2YwLTQ4ZjEtNGU0NS04MDBhLTgyZTg1ZDVmMjY1Yy9zZWNyZXRzIl19LCJhZG1pbiI6dHJ1ZSwiaWF0IjoxNTE2MjM5MDIyfQ.EjGpK28qgTHdKuGXXwPIwXf3XtlcX3-ZOOu73SMhJICOSl_niclr5KO8Uu0fMlfyotMYm7ekPSUt_jw8yem_WeHBx6ImcKq6o8pVdqmw3HuzUJTalrxEJSwaXxwtFr0QZe2womKLIq7jO0uI1mEYWvCDgdtVKF5rqwDuJv3rnCIxwSMwXv5HS9KluRIL4rAywu4U_SWAJQkmmxQGDC7TolvS8Ya3TCccjC34T6ze1R8T092Erdxa8uvs1SnmhvoAKhCW6E7qpUYTUJdLm3b4-xtQD8NOQsB8Ae_39nPvnpHfhj_JsQxWXhTwdpTM_24zg-0Dv6HFYhPFzNrfQy5TrQ"
)

func TestGetSecretPathsFromAssertionToken(t *testing.T) {
	tests := []struct {
		description  string
		token        string
		expectedVals []any
		expectedErr  error
	}{
		{
			description: "failed to parse JWT",
			token:       jwtNotParseable,
			expectedErr: errJWTParse,
		},
		{
			description: "no assertion claim",
			token:       jwtNoAssertion,
			expectedErr: errNoSecretPaths,
		},
		{
			description: "assertion wrong type",
			token:       jwtAssertionStr,
			expectedErr: errJWTParse,
		},
		{
			description: "no secretPaths key",
			token:       jwtNoSecretPaths,
			expectedErr: errNoSecretPaths,
		},
		{
			description: "secret paths wrong type",
			token:       jwtSecretPathsStr,
			expectedErr: errJWTParse,
		},
		{
			description: "empty secrets path list",
			token:       jwtEmptyPaths,
			expectedErr: errNoSecretPaths,
		},
		{
			description: "path list with secrets",
			token:       jwtAllPaths,
			expectedVals: []any{
				"accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/telemetry/6fa8366e-7ba2-4143-a744-7515faf9bf72",
				"accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/telemetry/6fa8366e-7ba2-4143-a744-7515faf9bf81",
				"accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/secrets",
				"tasks/136c2cf0-48f1-4e45-800a-82e85d5f265c/secrets",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			vals, err := getSecretPathsFromAssertionToken(tt.token)
			assert.ElementsMatch(t, tt.expectedVals, vals)
			if tt.expectedErr != nil {
				assert.ErrorIs(t, err, tt.expectedErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestNewESSAccountsSecretEnvs(t *testing.T) {
	destEnvVar := corev1.EnvVar{
		Name:  AccountsSecretsDestEnv,
		Value: EssAccountSecretsDest,
	}
	tests := []struct {
		description  string
		token        string
		expectedVars []corev1.EnvVar
		expectedErr  string
	}{
		{
			description: "only new account secret paths",
			token:       jwtNewAcctPaths,
			expectedVars: []corev1.EnvVar{
				{
					Name:  AccountsSecretsPathEnv,
					Value: "accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/telemetry/6fa8366e-7ba2-4143-a744-7515faf9bf72,accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/telemetry/6fa8366e-7ba2-4143-a744-7515faf9bf81",
				},
				destEnvVar,
			},
		},
		{
			description: "new and old account secret paths",
			token:       jwtAllPaths,
			expectedVars: []corev1.EnvVar{
				{
					Name:  AccountsSecretsPathEnv,
					Value: "accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/telemetry/6fa8366e-7ba2-4143-a744-7515faf9bf72,accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/telemetry/6fa8366e-7ba2-4143-a744-7515faf9bf81,accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/secrets",
				},
				destEnvVar,
			},
		},
		{
			description: "only old account secret path",
			token:       jwtOldAcctPath,
			expectedVars: []corev1.EnvVar{
				{
					Name:  AccountsSecretsPathEnv,
					Value: "accounts/_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq/secrets",
				},
				destEnvVar,
			},
		},
		{
			description: "only task secret path",
			token:       jwtTaskPath,
			expectedErr: errExpectedAccountSecrets.Error(),
		},
		{
			description: "bad token",
			token:       jwtNotParseable,
			expectedErr: "token is malformed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			envVars, err := NewESSAccountsSecretEnvs("_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq", tt.token, "tasks/136c2cf0-48f1-4e45-800a-82e85d5f265c/secrets")
			assert.Equal(t, tt.expectedVars, envVars)
			if len(tt.expectedErr) > 0 {
				assert.ErrorContains(t, err, tt.expectedErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}
