package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
)

const EncodedConfigVersion = 1

// setConfigEncodedComponents 包含需要序列化的 Oracle Config 对象的内容
type setConfigEncodedComponents struct {
	DeltaProgress           time.Duration
	DeltaResend             time.Duration
	DeltaRound              time.Duration
	DeltaGrace              time.Duration
	DeltaC                  time.Duration
	AlphaPPB                uint64
	DeltaStage              time.Duration
	RMax                    uint8
	S                       []int
	OffchainPublicKeys      []types.OffchainPublicKey
	PeerIDs                 []string
	SharedSecretEncryptions SharedSecretEncryptions
}

// setConfigSerializationTypes 给出了用于表示 setConfigEncodedComponents 的类型，以便进行 abi 编码。字段名称必须与 setConfigEncodedComponents 的字段名称匹配。
type setConfigSerializationTypes struct {
	DeltaProgress           int64
	DeltaResend             int64
	DeltaRound              int64
	DeltaGrace              int64
	DeltaC                  int64
	AlphaPPB                uint64
	DeltaStage              int64
	RMax                    uint8
	S                       []uint8
	OffchainPublicKeys      []common.Hash // 每个密钥都是 bytes32
	PeerIDs                 string        // 逗号分隔
	SharedSecretEncryptions sseSerializationTypes
}

// sseSerializationTypes 给出了用于表示 SharedSecretEncryptions 的类型，以便进行 abi 编码。字段名称必须与 SharedSecretEncryptions 的字段名称匹配。
type sseSerializationTypes struct {
	DiffieHellmanPoint common.Hash
	SharedSecretHash   common.Hash
	Encryptions        [][SharedSecretSize]byte
}

// encoding 是用于编码 setConfigEncodedComponents 的 ABI 模式，取自 setConfigEncodedComponentsABI（位于此包目录下的 abiencode.go 文件中）。
var encoding = getEncoding()

// Serialized configs 的大小不能超过此值（为了防止资源耗尽攻击而设置的任意限制）
var configSizeBound = 20 * 1000

// Encode 返回对象 o 的二进制序列化表示
func (o setConfigEncodedComponents) encode() []byte {
	rv, err := encoding.Pack(o.serializationRepresentation())
	if err != nil {
		panic(err)
	}
	if len(rv) > configSizeBound {
		panic("config 序列化太大")
	}
	return rv
}

func decodeContractSetConfigEncodedComponents(
	b []byte,
) (o setConfigEncodedComponents, err error) {
	if len(b) > configSizeBound {
		return o, errors.Errorf(
			"尝试反序列化过长的配置（%d 字节）", len(b),
		)
	}
	var vals []interface{}
	if vals, err = encoding.Unpack(b); err != nil {
		return o, errors.Wrapf(err, "无法反序列化 setConfig 二进制 blob")
	}
	setConfig := abi.ConvertType(vals[0], &setConfigSerializationTypes{}).(*setConfigSerializationTypes)
	return setConfig.golangRepresentation(), nil
}

func (o setConfigEncodedComponents) serializationRepresentation() setConfigSerializationTypes {
	transmitDelays := make([]uint8, len(o.S))
	for i, d := range o.S {
		transmitDelays[i] = uint8(d)
	}
	publicKeys := make([]common.Hash, len(o.OffchainPublicKeys))
	for i, k := range o.OffchainPublicKeys {
		publicKeys[i] = common.BytesToHash(k)
	}
	return setConfigSerializationTypes{
		int64(o.DeltaProgress),
		int64(o.DeltaResend),
		int64(o.DeltaRound),
		int64(o.DeltaGrace),
		int64(o.DeltaC),
		o.AlphaPPB,
		int64(o.DeltaStage),
		o.RMax,
		transmitDelays,
		publicKeys,
		strings.Join(o.PeerIDs, ","),
		o.SharedSecretEncryptions.serializationRepresentation(),
	}
}

func (or setConfigSerializationTypes) golangRepresentation() setConfigEncodedComponents {
	transmitDelays := make([]int, len(or.S))
	for i, d := range or.S {
		transmitDelays[i] = int(d)
	}
	keys := make([]types.OffchainPublicKey, len(or.OffchainPublicKeys))
	for i, k := range or.OffchainPublicKeys {
		keys[i] = types.OffchainPublicKey(k.Bytes())
	}
	var peerIDs []string
	if len(or.PeerIDs) > 0 {
		peerIDs = strings.Split(or.PeerIDs, ",")
	}
	return setConfigEncodedComponents{
		time.Duration(or.DeltaProgress),
		time.Duration(or.DeltaResend),
		time.Duration(or.DeltaRound),
		time.Duration(or.DeltaGrace),
		time.Duration(or.DeltaC),
		or.AlphaPPB,
		time.Duration(or.DeltaStage),
		or.RMax,
		transmitDelays,
		keys,
		peerIDs,
		or.SharedSecretEncryptions.golangRepresentation(),
	}
}

func (e SharedSecretEncryptions) serializationRepresentation() sseSerializationTypes {
	encs := make([][SharedSecretSize]byte, len(e.Encryptions))
	for i, enc := range e.Encryptions {
		encs[i] = enc
	}
	return sseSerializationTypes{
		common.Hash(e.DiffieHellmanPoint),
		e.SharedSecretHash,
		encs,
	}
}

func (er sseSerializationTypes) golangRepresentation() SharedSecretEncryptions {
	encs := make([]encryptedSharedSecret, len(er.Encryptions))
	for i, enc := range er.Encryptions {
		encs[i] = encryptedSharedSecret(enc)
	}
	return SharedSecretEncryptions{
		[32]byte(er.DiffieHellmanPoint),
		er.SharedSecretHash,
		encs,
	}
}

func getEncoding() abi.Arguments {
	// 在 abi 的 TestPack 中使用的技巧，用于解析参数列表：创建一个方法的 JSON 表示，该方法的输入为目标列表，然后从该方法中提取出解析的参数列表。
	aBI, err := abi.JSON(strings.NewReader(fmt.Sprintf(
		`[{ "name" : "method", "type": "function", "inputs": %s}]`,
		setConfigEncodedComponentsABI)))
	if err != nil {
		panic(err)
	}
	return aBI.Methods["method"].Inputs
}

func checkFieldNamesAgainstStruct(fields map[string]bool, i interface{}) {
	s := reflect.ValueOf(i).Type()
	for i := 0; i < s.NumField(); i++ {
		fieldName := s.Field(i).Name
		if !fields[fieldName] {
			panic("未找到" + fieldName + "的编码")
		}
		fields[fieldName] = false
	}
	for name, unseen := range fields {
		if unseen {
			panic("在 abiencode 模式中找到额外的字段，" + name)
		}
	}
}

func checkTupEntriesMatchStruct(t abi.Type, i interface{}) {
	if t.T != abi.TupleTy {
		panic("需要元组")
	}
	fields := make(map[string]bool)
	for _, fieldName := range t.TupleRawNames {
		capitalizedName := strings.ToUpper(fieldName[:1]) + fieldName[1:]
		fields[capitalizedName] = true
	}
	checkFieldNamesAgainstStruct(fields, i)
}

func init() { // 检查 abiencode 字段是否与 config 结构体匹配
	checkTupEntriesMatchStruct(encoding[0].Type, setConfigEncodedComponents{})
	components := encoding[0].Type.TupleElems
	essName := encoding[0].Type.TupleRawNames[len(components)-1]
	if essName != "sharedSecretEncryptions" {
		panic("期望 sharedSecretEncryptions 在最后的位置上，得到的是 " + essName)
	}
	ess := components[len(components)-1]
	checkTupEntriesMatchStruct(*ess, SharedSecretEncryptions{})
}

func checkFieldNamesMatch(s, t interface{}) {
	st, tt := reflect.ValueOf(s).Type(), reflect.ValueOf(t).Type()
	if st.NumField() != tt.NumField() {
		panic(fmt.Sprintf("字段数量不匹配：%T 有 %d 个字段，%T 有 %d 个字段",
			s, st.NumField(),
			t, tt.NumField()))
	}
	for i := 0; i < st.NumField(); i++ {
		if st.Field(i).Name != tt.Field(i).Name {
			panic(fmt.Sprintf("字段名称在 %T 与 %T 不匹配：%s vs %s",
				s, t, st.Field(i).Name, tt.Field(i).Name))
		}
	}
}

func init() { // 检查序列化字段是否与目标结构体匹配
	checkFieldNamesMatch(setConfigEncodedComponents{}, setConfigSerializationTypes{})
	checkFieldNamesMatch(SharedSecretEncryptions{}, sseSerializationTypes{})
}
