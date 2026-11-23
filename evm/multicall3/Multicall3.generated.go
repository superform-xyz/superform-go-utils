// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package multicall3

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
	_ = abi.ConvertType
)

// Multicall3Call3 is an auto generated low-level Go binding around an user-defined struct.
type Multicall3Call3 struct {
	Target       common.Address
	AllowFailure bool
	CallData     []byte
}

// Multicall3Result is an auto generated low-level Go binding around an user-defined struct.
type Multicall3Result struct {
	Success    bool
	ReturnData []byte
}

// Multicall3MetaData contains all meta data concerning the Multicall3 contract.
var Multicall3MetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"target\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"allowFailure\",\"type\":\"bool\"},{\"internalType\":\"bytes\",\"name\":\"callData\",\"type\":\"bytes\"}],\"internalType\":\"structMulticall3.Call3[]\",\"name\":\"calls\",\"type\":\"tuple[]\"}],\"name\":\"aggregate3\",\"outputs\":[{\"components\":[{\"internalType\":\"bool\",\"name\":\"success\",\"type\":\"bool\"},{\"internalType\":\"bytes\",\"name\":\"returnData\",\"type\":\"bytes\"}],\"internalType\":\"structMulticall3.Result[]\",\"name\":\"returnData\",\"type\":\"tuple[]\"}],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"addr\",\"type\":\"address\"}],\"name\":\"getEthBalance\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

// Multicall3ABI is the input ABI used to generate the binding from.
// Deprecated: Use Multicall3MetaData.ABI instead.
var Multicall3ABI = Multicall3MetaData.ABI

// Multicall3 is an auto generated Go binding around an Ethereum contract.
type Multicall3 struct {
	Multicall3Caller     // Read-only binding to the contract
	Multicall3Transactor // Write-only binding to the contract
	Multicall3Filterer   // Log filterer for contract events
}

// Multicall3Caller is an auto generated read-only Go binding around an Ethereum contract.
type Multicall3Caller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// Multicall3Transactor is an auto generated write-only Go binding around an Ethereum contract.
type Multicall3Transactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// Multicall3Filterer is an auto generated log filtering Go binding around an Ethereum contract events.
type Multicall3Filterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// Multicall3Session is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type Multicall3Session struct {
	Contract     *Multicall3       // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// Multicall3CallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type Multicall3CallerSession struct {
	Contract *Multicall3Caller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts     // Call options to use throughout this session
}

// Multicall3TransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type Multicall3TransactorSession struct {
	Contract     *Multicall3Transactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts     // Transaction auth options to use throughout this session
}

// Multicall3Raw is an auto generated low-level Go binding around an Ethereum contract.
type Multicall3Raw struct {
	Contract *Multicall3 // Generic contract binding to access the raw methods on
}

// Multicall3CallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type Multicall3CallerRaw struct {
	Contract *Multicall3Caller // Generic read-only contract binding to access the raw methods on
}

// Multicall3TransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type Multicall3TransactorRaw struct {
	Contract *Multicall3Transactor // Generic write-only contract binding to access the raw methods on
}

// NewMulticall3 creates a new instance of Multicall3, bound to a specific deployed contract.
func NewMulticall3(address common.Address, backend bind.ContractBackend) (*Multicall3, error) {
	contract, err := bindMulticall3(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Multicall3{Multicall3Caller: Multicall3Caller{contract: contract}, Multicall3Transactor: Multicall3Transactor{contract: contract}, Multicall3Filterer: Multicall3Filterer{contract: contract}}, nil
}

// NewMulticall3Caller creates a new read-only instance of Multicall3, bound to a specific deployed contract.
func NewMulticall3Caller(address common.Address, caller bind.ContractCaller) (*Multicall3Caller, error) {
	contract, err := bindMulticall3(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &Multicall3Caller{contract: contract}, nil
}

// NewMulticall3Transactor creates a new write-only instance of Multicall3, bound to a specific deployed contract.
func NewMulticall3Transactor(address common.Address, transactor bind.ContractTransactor) (*Multicall3Transactor, error) {
	contract, err := bindMulticall3(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &Multicall3Transactor{contract: contract}, nil
}

// NewMulticall3Filterer creates a new log filterer instance of Multicall3, bound to a specific deployed contract.
func NewMulticall3Filterer(address common.Address, filterer bind.ContractFilterer) (*Multicall3Filterer, error) {
	contract, err := bindMulticall3(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &Multicall3Filterer{contract: contract}, nil
}

// bindMulticall3 binds a generic wrapper to an already deployed contract.
func bindMulticall3(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := Multicall3MetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Multicall3 *Multicall3Raw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Multicall3.Contract.Multicall3Caller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Multicall3 *Multicall3Raw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Multicall3.Contract.Multicall3Transactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Multicall3 *Multicall3Raw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Multicall3.Contract.Multicall3Transactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Multicall3 *Multicall3CallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Multicall3.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Multicall3 *Multicall3TransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Multicall3.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Multicall3 *Multicall3TransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Multicall3.Contract.contract.Transact(opts, method, params...)
}

// GetEthBalance is a free data retrieval call binding the contract method 0x4d2301cc.
//
// Solidity: function getEthBalance(address addr) view returns(uint256 balance)
func (_Multicall3 *Multicall3Caller) GetEthBalance(opts *bind.CallOpts, addr common.Address) (*big.Int, error) {
	var out []interface{}
	err := _Multicall3.contract.Call(opts, &out, "getEthBalance", addr)

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetEthBalance is a free data retrieval call binding the contract method 0x4d2301cc.
//
// Solidity: function getEthBalance(address addr) view returns(uint256 balance)
func (_Multicall3 *Multicall3Session) GetEthBalance(addr common.Address) (*big.Int, error) {
	return _Multicall3.Contract.GetEthBalance(&_Multicall3.CallOpts, addr)
}

// GetEthBalance is a free data retrieval call binding the contract method 0x4d2301cc.
//
// Solidity: function getEthBalance(address addr) view returns(uint256 balance)
func (_Multicall3 *Multicall3CallerSession) GetEthBalance(addr common.Address) (*big.Int, error) {
	return _Multicall3.Contract.GetEthBalance(&_Multicall3.CallOpts, addr)
}

// Aggregate3 is a paid mutator transaction binding the contract method 0x82ad56cb.
//
// Solidity: function aggregate3((address,bool,bytes)[] calls) payable returns((bool,bytes)[] returnData)
func (_Multicall3 *Multicall3Transactor) Aggregate3(opts *bind.TransactOpts, calls []Multicall3Call3) (*types.Transaction, error) {
	return _Multicall3.contract.Transact(opts, "aggregate3", calls)
}

// Aggregate3 is a paid mutator transaction binding the contract method 0x82ad56cb.
//
// Solidity: function aggregate3((address,bool,bytes)[] calls) payable returns((bool,bytes)[] returnData)
func (_Multicall3 *Multicall3Session) Aggregate3(calls []Multicall3Call3) (*types.Transaction, error) {
	return _Multicall3.Contract.Aggregate3(&_Multicall3.TransactOpts, calls)
}

// Aggregate3 is a paid mutator transaction binding the contract method 0x82ad56cb.
//
// Solidity: function aggregate3((address,bool,bytes)[] calls) payable returns((bool,bytes)[] returnData)
func (_Multicall3 *Multicall3TransactorSession) Aggregate3(calls []Multicall3Call3) (*types.Transaction, error) {
	return _Multicall3.Contract.Aggregate3(&_Multicall3.TransactOpts, calls)
}
