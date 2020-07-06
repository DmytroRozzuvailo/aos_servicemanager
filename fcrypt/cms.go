// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fcrypt

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"math/big"

	log "github.com/sirupsen/logrus"
)

type asnContentInfo struct {
	OID           asn1.ObjectIdentifier
	EnvelopedData asnEnvelopedData `asn1:"explicit,tag:0"`
}

type asnEnvelopedData struct {
	Version              int
	OriginatorInfo       asnOriginatorInfo `asn1:"optional,implicit,tag:0"`
	RecipientInfos       []asn1.RawValue   `asn1:"set"`
	EncryptedContentInfo EncryptedContentInfo
	UnprotectedAttrs     []asn1.RawValue `asn1:"optional,implicit,tag:1,set"`
}

type asnOriginatorInfo struct {
	Certs asn1.RawValue `asn1:"optional,implicit,tag:0"`
	Crls  asn1.RawValue `asn1:"optional,implicit,tag:1"`
}

//EncryptedContentInfo User-friendly structures
type EncryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           []byte `asn1:"optional,implicit,tag:0"`
}

type issuerAndSerialNumber struct {
	Issuer       asn1.RawValue `asn1:"sequence"`
	SerialNumber *big.Int
}

type contentInfo struct {
	OID           asn1.ObjectIdentifier
	EnvelopedData envelopedData
}

type envelopedData struct {
	Version              int
	OriginatorInfo       originatorInfo
	RecipientInfos       []interface{}
	EncryptedContentInfo EncryptedContentInfo
	UnprotectedAttrs     interface{}
}

type originatorInfo struct {
	Certs []interface{}
	Crls  []interface{}
}

type keyTransRecipientInfo struct {
	Version                int
	Rid                    issuerAndSerialNumber
	KeyEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedKey           []byte
}

var (
	envelopedDataOid = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	rsaEncryptionOid = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	aes256CbcOid     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

func getRecipientInfo(raw asn1.RawValue) (interface{}, error) {
	switch raw.Tag {
	case 16:
		var ktri keyTransRecipientInfo
		_, err := asn1.Unmarshal(raw.FullBytes, &ktri)
		if err != nil {
			return nil, err
		}
		return ktri, nil

	default:
		return nil, errors.New("getRecipientInfo: unknown tag")
	}
}

func getOriginatorInfo(oi asnOriginatorInfo) (*originatorInfo, error) {
	var ret originatorInfo

	return &ret, nil
}

func getEnvelopedData(ed asnEnvelopedData) (*envelopedData, error) {
	var ret envelopedData

	ret.Version = ed.Version
	oi, err := getOriginatorInfo(ed.OriginatorInfo)
	if err != nil {
		return nil, err
	}

	ret.OriginatorInfo = *oi
	ret.RecipientInfos = make([]interface{}, len(ed.RecipientInfos))
	for i, recipient := range ed.RecipientInfos {
		ret.RecipientInfos[i], err = getRecipientInfo(recipient)
		if err != nil {
			return nil, err
		}
	}
	ret.EncryptedContentInfo = ed.EncryptedContentInfo

	return &ret, nil
}

func getContentInfo(ci asnContentInfo) (*contentInfo, error) {
	var ret contentInfo

	ret.OID = ci.OID
	ed, err := getEnvelopedData(ci.EnvelopedData)
	if err != nil {
		return nil, err
	}

	ret.EnvelopedData = *ed

	return &ret, nil
}

func decryptCMSKey(ktri *keyTransRecipientInfo, decryptor crypto.Decrypter) (symmetrickey []byte, err error) {
	if !ktri.KeyEncryptionAlgorithm.Algorithm.Equal(rsaEncryptionOid) {
		return nil, errors.New("unknown public encryption OID")
	}

	if ktri.KeyEncryptionAlgorithm.Parameters.Tag != asn1.TagNull {
		return nil, errors.New("extra paramaters for RSA algorithm found")
	}

	symmetrickey, err = decryptor.Decrypt(nil, ktri.EncryptedKey, nil)
	if err != nil {
		return nil, err
	}

	log.Debugf("AES KEY: %#v", symmetrickey)

	return symmetrickey, nil
}

func decryptMessage(eci *EncryptedContentInfo, key []byte) ([]byte, error) {
	switch {
	case eci.ContentEncryptionAlgorithm.Algorithm.Equal(aes256CbcOid):
		if eci.ContentEncryptionAlgorithm.Parameters.Tag != asn1.TagOctetString {
			return nil, errors.New("can't find IV in extended params")
		}

		iv := eci.ContentEncryptionAlgorithm.Parameters.Bytes
		if len(iv) != 16 {
			return nil, errors.New("invalid IV length")
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			log.Errorf("Can't create cipher: %s", err)
			return nil, err
		}

		mode := cipher.NewCBCDecrypter(block, iv)
		outdata := make([]byte, len(eci.EncryptedContent))
		mode.CryptBlocks(outdata, eci.EncryptedContent)

		return removePkcs7Padding(outdata, 16)

	default:
		return nil, errors.New("unknown symmetric algorithm OID")
	}
}

func unmarshallCMS(der []byte) (*contentInfo, error) {
	var ci asnContentInfo

	_, err := asn1.Unmarshal(der, &ci)
	if err != nil {
		log.Errorf("Error parsing CMS container: %s", err)
		return nil, err
	}

	if !ci.OID.Equal(envelopedDataOid) {
		return nil, errors.New("unknown object identifier in ContentInfo")
	}

	return getContentInfo(ci)
}
