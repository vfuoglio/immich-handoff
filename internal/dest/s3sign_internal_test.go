package dest

import (
	"net/http"
	"testing"
	"time"
)

// The example in Amazon's documentation of signing a request ("Signature Calculations for the Authorization Header: Transferring Payload
// in a Single Chunk"): a GET of /test.txt with a Range header, on 24 May 2013, with the documentation's example keys.
func TestTheSignatureOfAnS3RequestIsTheOneAmazonDocuments(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	signV4(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", emptyPayloadSum, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestPathsAndQueriesAreEncodedTheWayAmazonSays(t *testing.T) {
	if got := uriEncode("a b/ü(1).jpg", true); got != "a%20b/%C3%BC%281%29.jpg" {
		t.Errorf("%q", got)
	}
	if got := uriEncode("a/b", false); got != "a%2Fb" {
		t.Errorf("%q", got)
	}
}
