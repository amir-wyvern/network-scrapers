package main

import (
    "github.com/influxdata/influxdb-client-go/v2"
	"github.com/ethereum/go-ethereum/accounts/abi"  
    "github.com/ethereum/go-ethereum/core/types"
    "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/common"
    "github.com/thedevsaddam/iter"
    "github.com/joho/godotenv"
	"encoding/json"
	"io/ioutil"
    "math/big"
    "context"
    "bytes"
    "sync"
    "time"
    "fmt"
    "log"
    "os"
)

var TOPICS = [1]common.Hash{common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")}
var NONE_ADDRESS common.Address = common.HexToAddress("0x0000000000000000000000000000000000000000")
var INFLUX_CLI = influxdb2.NewClient("","") 
var WHITE_LIST_ADDRESS sync.Map 
var BLACK_LIST_ADDRESS sync.Map
var CLIENT *ethclient.Client
var NUMBER_TX_IN_BLOCK uint32 = 20
var NUMBER_WAIT_PERIOD uint32 = 2
var WORKER_NUMBER uint32 = 100

var CREATE = map[[4]byte]interface{} {
    [4]byte{0x60, 0x80, 0x60, 0x40} : func (_ *big.Int, tx *types.Transaction)(*big.Int,string){return big.NewInt(0), ""},
}
var ADD_LIQ = map[[4]byte]interface{} {
	[4]byte{0xf3, 0x05, 0xd7, 0x19} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, ""} ,
}
var SWAP = map[[4]byte]interface{} {
	[4]byte{0x18, 0xcb, 0xaf, 0xe5} : func(_ *big.Int, tx *types.Transaction)(*big.Int,string){return DecodeTransactionInputData(CONTRACT_ABI, tx.Data())["amountOutMin"].(*big.Int), "sell"} ,
	[4]byte{0x79, 0x1a, 0xc9, 0x47} : func(_ *big.Int, tx *types.Transaction)(*big.Int,string){return DecodeTransactionInputData(CONTRACT_ABI, tx.Data())["amountOutMin"].(*big.Int), "sell"} ,
	[4]byte{0x4a, 0x25, 0xd9, 0x4a} : func(_ *big.Int, tx *types.Transaction)(*big.Int,string){return DecodeTransactionInputData(CONTRACT_ABI, tx.Data())["amountOut"].(*big.Int), "sell"},
	[4]byte{0x7f, 0xf3, 0x6a, 0xb5} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, "buy"} ,
	[4]byte{0xfb, 0x3b, 0xdb, 0x41} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, "buy"} ,
	[4]byte{0xb6, 0xf9, 0xde, 0x95} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, "buy"} ,
}
var REMOVE_LIQ = map[[4]byte]interface{} {
	[4]byte{0x02, 0x75, 0x1c, 0xec} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, ""} ,
	[4]byte{0xaf, 0x29, 0x79, 0xeb} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, ""} ,
	[4]byte{0xde, 0xd9, 0x38, 0x2a} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, ""} ,
	[4]byte{0x5b, 0x0d, 0x59, 0x84} : func(value *big.Int, tx *types.Transaction)(*big.Int,string){return value, ""} ,
}


var CONTRACT_ABI = GetContractABI()
func GetContractABI() *abi.ABI {

	jsonFile, err := os.Open("contract-router-pancake.json")

	if err != nil {
        fmt.Println(err)
    }
    defer jsonFile.Close()

	byteValue, _ := ioutil.ReadAll(jsonFile)
	
	var RawABI map[string]interface{}
    json.Unmarshal([]byte(byteValue), &RawABI)

	byteABI ,_:= json.Marshal(RawABI["abi"])
	
	reader := bytes.NewReader(byteABI)
	buf := make([]byte, len(byteABI))
	_, err2 := reader.Read(buf)
	if err2 != nil {
	  log.Fatal(err2)
	}
	
	contractABI, err := abi.JSON(bytes.NewReader(buf))
	if err != nil {
		log.Fatal(err)
	}

	return &contractABI
}

func DecodeTransactionInputData(contractABI *abi.ABI, data []byte) map[string]interface{} {
	
    methodSigData := data[:4]
	inputsSigData := data[4:]
	method, err := contractABI.MethodById(methodSigData)
	if err != nil {
		log.Fatal(err)
	}
	inputsMap := make(map[string]interface{})
	if err := method.Inputs.UnpackIntoMap(inputsMap, inputsSigData); err != nil {
		log.Fatal(err)
	} 
	return inputsMap
}

// ============= ReviewTx
func SpinupWorkerForReviewTx(
    count int,
    TxPipline chan StructTxPipline,
    reviewTxPipline <-chan StructTxPipline,
    currentBlockNumberPipline <-chan uint64) {    
    
    var currentBlockNumber uint64
    var mutexCurrentBlockNumber sync.Mutex

    var constDiffNumberOfBlocks uint64 = uint64(WORKER_NUMBER * NUMBER_WAIT_PERIOD)
    
    // this is functions just for SpinupWorkerForReviewTx function 
    filterTx := func(txWithBlockTime StructTxPipline) {

        tx := txWithBlockTime.tx
        key4Byte := [4]byte{tx.Data()[0], tx.Data()[1], tx.Data()[2], tx.Data()[3]} 
        
        txForm, _ := FormingTx(key4Byte)
        contactAddress := txForm.ContractAddress(tx)

        if contactAddress != NONE_ADDRESS  {
            fmt.Println("========================== : " ,txWithBlockTime.tx.Hash().Hex())
            TxPipline <- txWithBlockTime

        }else {
            _, exist := BLACK_LIST_ADDRESS.Load(contactAddress)
            if !exist {
                fmt.Println("!!!!!!!!!!!!!!!!!!!! : " ,txWithBlockTime.tx.Hash().Hex())
                BLACK_LIST_ADDRESS.Store(contactAddress, true)
            }
        }
    }
    conditionOpenChanal := func(txBlockNumber uint64)bool {
        mutexCurrentBlockNumber.Lock()
        condition := txBlockNumber - currentBlockNumber >= constDiffNumberOfBlocks
        mutexCurrentBlockNumber.Unlock()

        return condition
    }
    startGetTxFromChanal := func(txWithBlockTime StructTxPipline) StructTxPipline { 

        filterTx(txWithBlockTime)
        nextTxWithBlockTime := txWithBlockTime
        // var nextTxWithBlockTime StructTxPipline
        for txWithBlockTime := range reviewTxPipline { 
            if conditionOpenChanal(txWithBlockTime.blockNumber){
                filterTx(txWithBlockTime)
            } else {
                nextTxWithBlockTime = txWithBlockTime
                break
            }
        }

        return nextTxWithBlockTime
    }
    // end functions

    go func () {
        for blockNumber := range currentBlockNumberPipline{
            mutexCurrentBlockNumber.Lock()
            currentBlockNumber = blockNumber
            mutexCurrentBlockNumber.Unlock()
            // fmt.Println("block current : " ,x)
        }
    }()

    go func() {

        // var txWithBlockTime StructTxPipline
        txWithBlockTime := <-reviewTxPipline
        for true{
            if conditionOpenChanal(txWithBlockTime.blockNumber) {
                startGetTxFromChanal(txWithBlockTime)
            }
            time.Sleep(time.Second / 2)
        }
    }()

    // This function is executed when the channel is full ,For emergencies
    go func (){
        maxChanalSize := WORKER_NUMBER * NUMBER_WAIT_PERIOD * NUMBER_TX_IN_BLOCK
        for true {
            if uint32(len(reviewTxPipline)) > (maxChanalSize*9/10) {
                count := uint32(len(reviewTxPipline)) - (maxChanalSize*9/10)
                for i := uint32(0) ; i < count ; i++ {
                    txWithBlockTime := <- reviewTxPipline
                    filterTx(txWithBlockTime)
                }
            }
            time.Sleep(time.Second / 2)
        }
    }()
}
// ============= end

// ============= GetTx
func ExtractAddressFromRemoveLiqudity(tx *types.Transaction) common.Address {
    inputABI := DecodeTransactionInputData(CONTRACT_ABI, tx.Data())
    contactAddress := inputABI["token"].(common.Address)
    
    return contactAddress
}
func ExtractAddressFromAddLiqudity(tx *types.Transaction) common.Address {
    inputABI := DecodeTransactionInputData(CONTRACT_ABI, tx.Data())
    contactAddress := inputABI["token"].(common.Address)
    
    return contactAddress
}
func ExtractAddressFromCreate(tx *types.Transaction) common.Address {
    receipt, _ := CLIENT.TransactionReceipt(context.Background(), tx.Hash())
    return receipt.ContractAddress
}
func ExtractAddressFromSwap(tx *types.Transaction) common.Address {

    inputABI := DecodeTransactionInputData(CONTRACT_ABI, tx.Data()) 

    contactAddress := NONE_ADDRESS
    for _, address := range inputABI["path"].([]common.Address) {
        _, exist := WHITE_LIST_ADDRESS.Load(address)
        if exist{
            contactAddress = address
            break
        }
    }

    return contactAddress
}

func FormingTxForInflux(
    mem string,
    contractAddress common.Address,
    sender common.Address,
    swapType string,
    amount float32,
    time time.Time ) {
    
    writeAPI := INFLUX_CLI.WriteAPI("org", "BSC_Scraping")
    point :=influxdb2.NewPointWithMeasurement(mem).
        AddTag("contractAddress", contractAddress.Hex()).
        AddTag("sender", sender.Hex()).
        AddTag("swapType", swapType).
        AddField("amount", amount).
        SetTime(time)
    
    writeAPI.WritePoint(point)
}

type TxFunctions struct {
    SendToInflux func(string,common.Address,common.Address,string,float32,time.Time)
    ContractAddress func(*types.Transaction)common.Address
    ValueAndSwapType map[[4]byte]interface{}
    txType string
}
func FormingTx(key4Byte [4]byte) (TxFunctions, bool) {
    
    formResponse := TxFunctions{
        SendToInflux: FormingTxForInflux,
    }

    if SWAP[key4Byte] != nil {

        formResponse.ContractAddress = ExtractAddressFromSwap
        formResponse.ValueAndSwapType = SWAP
        formResponse.txType = "swap"
        return formResponse, false
        
    } else if CREATE[key4Byte] != nil {
        
        formResponse.ContractAddress = ExtractAddressFromCreate
        formResponse.ValueAndSwapType = CREATE
        formResponse.txType = "create"
        return formResponse, false

    } else if ADD_LIQ[key4Byte] != nil {
        
        formResponse.ContractAddress = ExtractAddressFromAddLiqudity
        formResponse.ValueAndSwapType = ADD_LIQ
        formResponse.txType = "addLiquidity"
        return formResponse, false
    
    } else if REMOVE_LIQ[key4Byte] != nil {
        
        formResponse.ContractAddress = ExtractAddressFromRemoveLiqudity
        formResponse.ValueAndSwapType = REMOVE_LIQ
        formResponse.txType = "removeLiquidity"
        return formResponse, false

    } else {
        return TxFunctions{}, true
    }
}

func AnalyzeTx(txWithBlockTime StructTxPipline, reviewTxPipline chan StructTxPipline ) {

    tx := txWithBlockTime.tx
    key4Byte := [4]byte{tx.Data()[0], tx.Data()[1], tx.Data()[2], tx.Data()[3]} 

    txForm, err := FormingTx(key4Byte)

    if !err {
        // sender, _ := types.Sender(types.NewEIP155Signer(tx.ChainId()), tx)
        value, _ := txForm.ValueAndSwapType[key4Byte].(func(*big.Int, *types.Transaction)(*big.Int,string))(tx.Value(), tx)
        
        if key4Byte == [4]byte{0x60, 0x80, 0x60, 0x40} {
            receipt, _ := CLIENT.TransactionReceipt(context.Background(), tx.Hash())
            if IsContainTopicsHash(receipt.Logs) {

                WHITE_LIST_ADDRESS.Store(receipt.ContractAddress, true)
                // txForm.FormingTxForInflux(txForm.txType ,...)
            }

        } else {
            contractAddress := txForm.ContractAddress(tx)
            _, exist := WHITE_LIST_ADDRESS.Load(contractAddress)
            if exist {
                fmt.Println(txForm.txType , " : " ,value)
                // txForm.FormingTxForInflux(txForm.txType,...)
            } else {
                _, exist := BLACK_LIST_ADDRESS.Load(contractAddress)
                if !exist {
                    reviewTxPipline <- txWithBlockTime
                }
            }
        }
    }
    
}

func IsContainTopicsHash(logs []*types.Log) bool {
    for _, log := range logs {
        for _, topic := range log.Topics {
            for _, hash := range TOPICS {
                if hash == topic {
                    return true
                }
            }
        }
    }
    return false 
}

func SpinupWorkerForGetTx(count uint32, txPipline <-chan StructTxPipline, reviewTxPipline chan StructTxPipline) {
    for i := uint32(0); i < count; i++ {
        go func () {
            for txWithBlockTime := range txPipline { 
                AnalyzeTx(txWithBlockTime, reviewTxPipline)
            }
        }()
    }
}

// ============= end

// ============= GetBlock 
func GetBlockNumber(number uint64) (types.Transactions, uint64) {

    fmt.Printf("> %d \n", number)

    blockNumber := big.NewInt(int64(number))
    block, err := CLIENT.BlockByNumber(context.Background(), blockNumber)

    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf( "<%d \n" ,block.Number().Uint64())

    return block.Transactions(), block.Header().Time
}

type StructTxPipline struct {
    tx *types.Transaction
    blockTime uint64
    blockNumber uint64
}
func SendTxToPipline(blockTxs types.Transactions, blockTime uint64, blockNumber uint64, txPipline chan StructTxPipline) {

    for _, tx := range blockTxs {
        if len(tx.Data()) >= 4 {
                
            txWithBlockTime := StructTxPipline{tx:tx, blockTime:blockTime, blockNumber:blockNumber }
            txPipline <- txWithBlockTime
        }
    }
}

func SpinupWorkerForGetBlock(
    count uint32,
    blockNumberPipline <-chan uint64,
    txPipline chan StructTxPipline,
    currentBlockNumberPipline chan uint64,
    wg *sync.WaitGroup,) {

    for i := uint32(0); i < count; i++ {
        wg.Add(1)
        go func () {
            for blockNumber := range blockNumberPipline {
                blockTxs, blockTime := GetBlockNumber(blockNumber)
                SendTxToPipline(blockTxs, blockTime, blockNumber, txPipline)
                currentBlockNumberPipline <- blockNumber
            }
            wg.Done()
        }()
    }
}
// ============= end



func main() {

    startTime := time.Now()
    
    err := godotenv.Load()
    if err != nil {
      log.Fatal("Error loading .env file")
    }
    
    CLIENT, _ = ethclient.Dial("https://bsc-dataseed.binance.org")
    influxToken := os.Getenv("TOEKN") 
    INFLUX_CLI = influxdb2.NewClient("http://localhost:8086", influxToken ) 

    wg := &sync.WaitGroup{}

    blockNumberPipline := make(chan uint64)
    chanleSize := NUMBER_TX_IN_BLOCK * WORKER_NUMBER * (NUMBER_WAIT_PERIOD + 1)
    reviewTxPipline := make(chan StructTxPipline, chanleSize)
    txPipline := make(chan StructTxPipline)
    currentBlockNumberPipline := make(chan uint64)
    

    SpinupWorkerForGetBlock(WORKER_NUMBER, blockNumberPipline, txPipline, currentBlockNumberPipline, wg) 
    getTxWorkerNumber := uint32(2)
    if WORKER_NUMBER > 20 {getTxWorkerNumber = WORKER_NUMBER / 10 }
    SpinupWorkerForGetTx(getTxWorkerNumber, txPipline, reviewTxPipline) 
    SpinupWorkerForReviewTx(1, txPipline, reviewTxPipline, currentBlockNumberPipline) 
    
    for i := range iter.N(21061418,21071407) {
        blockNumberPipline <- uint64(i)
    }

    close(blockNumberPipline)
    wg.Wait()
    fmt.Printf("Binomial took %s", time.Since(startTime))

}