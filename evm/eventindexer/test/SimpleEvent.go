// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package test

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

// SimpleEventMetaData contains all meta data concerning the SimpleEvent contract.
var SimpleEventMetaData = &bind.MetaData{
	ABI: "[{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"sender\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256[]\",\"name\":\"values\",\"type\":\"uint256[]\"}],\"name\":\"MultipleValuesChanged\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"sender\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"}],\"name\":\"ValueChanged\",\"type\":\"event\"},{\"inputs\":[],\"name\":\"getValue\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256[]\",\"name\":\"_values\",\"type\":\"uint256[]\"}],\"name\":\"setMultipleValues\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_value\",\"type\":\"uint256\"}],\"name\":\"setValue\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
	Bin: "0x6080604052348015600f57600080fd5b506105118061001f6000396000f3fe608060405234801561001057600080fd5b50600436106100415760003560e01c806320965255146100465780635524107714610064578063ade8b1ed14610080575b600080fd5b61004e61009c565b60405161005b91906101a2565b60405180910390f35b61007e600480360381019061007991906101fd565b6100a5565b005b61009a60048036038101906100959190610383565b6100fd565b005b60008054905090565b806000819055503373ffffffffffffffffffffffffffffffffffffffff167fe2b64d6eb95e02b6354c9dad0f0b75f05c8659bc5f2a0f91bc457fc27c55c688826040516100f291906101a2565b60405180910390a250565b60005b81518110156101375781818151811061011c5761011b6103cc565b5b60200260200101516000819055508080600101915050610100565b503373ffffffffffffffffffffffffffffffffffffffff167f01bc48a284338ab1975c9290479dd8e37c372682c555dd104e5cd6be96dfe2378260405161017e91906104b9565b60405180910390a250565b6000819050919050565b61019c81610189565b82525050565b60006020820190506101b76000830184610193565b92915050565b6000604051905090565b600080fd5b600080fd5b6101da81610189565b81146101e557600080fd5b50565b6000813590506101f7816101d1565b92915050565b600060208284031215610213576102126101c7565b5b6000610221848285016101e8565b91505092915050565b600080fd5b6000601f19601f8301169050919050565b7f4e487b7100000000000000000000000000000000000000000000000000000000600052604160045260246000fd5b6102788261022f565b810181811067ffffffffffffffff8211171561029757610296610240565b5b80604052505050565b60006102aa6101bd565b90506102b6828261026f565b919050565b600067ffffffffffffffff8211156102d6576102d5610240565b5b602082029050602081019050919050565b600080fd5b60006102ff6102fa846102bb565b6102a0565b90508083825260208201905060208402830185811115610322576103216102e7565b5b835b8181101561034b578061033788826101e8565b845260208401935050602081019050610324565b5050509392505050565b600082601f83011261036a5761036961022a565b5b813561037a8482602086016102ec565b91505092915050565b600060208284031215610399576103986101c7565b5b600082013567ffffffffffffffff8111156103b7576103b66101cc565b5b6103c384828501610355565b91505092915050565b7f4e487b7100000000000000000000000000000000000000000000000000000000600052603260045260246000fd5b600081519050919050565b600082825260208201905092915050565b6000819050602082019050919050565b61043081610189565b82525050565b60006104428383610427565b60208301905092915050565b6000602082019050919050565b6000610466826103fb565b6104708185610406565b935061047b83610417565b8060005b838110156104ac5781516104938882610436565b975061049e8361044e565b92505060018101905061047f565b5085935050505092915050565b600060208201905081810360008301526104d3818461045b565b90509291505056fea26469706673582212208e547ddeda8d603109d0adb0627ec3c2ffdf5c966c030d3d216904986bc8dbe564736f6c634300081c0033",
}

// SimpleEventABI is the input ABI used to generate the binding from.
// Deprecated: Use SimpleEventMetaData.ABI instead.
var SimpleEventABI = SimpleEventMetaData.ABI

// SimpleEventBin is the compiled bytecode used for deploying new contracts.
// Deprecated: Use SimpleEventMetaData.Bin instead.
var SimpleEventBin = SimpleEventMetaData.Bin

// DeploySimpleEvent deploys a new Ethereum contract, binding an instance of SimpleEvent to it.
func DeploySimpleEvent(auth *bind.TransactOpts, backend bind.ContractBackend) (common.Address, *types.Transaction, *SimpleEvent, error) {
	parsed, err := SimpleEventMetaData.GetAbi()
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	if parsed == nil {
		return common.Address{}, nil, nil, errors.New("GetABI returned nil")
	}

	address, tx, contract, err := bind.DeployContract(auth, *parsed, common.FromHex(SimpleEventBin), backend)
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	return address, tx, &SimpleEvent{SimpleEventCaller: SimpleEventCaller{contract: contract}, SimpleEventTransactor: SimpleEventTransactor{contract: contract}, SimpleEventFilterer: SimpleEventFilterer{contract: contract}}, nil
}

// SimpleEvent is an auto generated Go binding around an Ethereum contract.
type SimpleEvent struct {
	SimpleEventCaller     // Read-only binding to the contract
	SimpleEventTransactor // Write-only binding to the contract
	SimpleEventFilterer   // Log filterer for contract events
}

// SimpleEventCaller is an auto generated read-only Go binding around an Ethereum contract.
type SimpleEventCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SimpleEventTransactor is an auto generated write-only Go binding around an Ethereum contract.
type SimpleEventTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SimpleEventFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type SimpleEventFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SimpleEventSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type SimpleEventSession struct {
	Contract     *SimpleEvent      // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// SimpleEventCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type SimpleEventCallerSession struct {
	Contract *SimpleEventCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts      // Call options to use throughout this session
}

// SimpleEventTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type SimpleEventTransactorSession struct {
	Contract     *SimpleEventTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts      // Transaction auth options to use throughout this session
}

// SimpleEventRaw is an auto generated low-level Go binding around an Ethereum contract.
type SimpleEventRaw struct {
	Contract *SimpleEvent // Generic contract binding to access the raw methods on
}

// SimpleEventCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type SimpleEventCallerRaw struct {
	Contract *SimpleEventCaller // Generic read-only contract binding to access the raw methods on
}

// SimpleEventTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type SimpleEventTransactorRaw struct {
	Contract *SimpleEventTransactor // Generic write-only contract binding to access the raw methods on
}

// NewSimpleEvent creates a new instance of SimpleEvent, bound to a specific deployed contract.
func NewSimpleEvent(address common.Address, backend bind.ContractBackend) (*SimpleEvent, error) {
	contract, err := bindSimpleEvent(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &SimpleEvent{SimpleEventCaller: SimpleEventCaller{contract: contract}, SimpleEventTransactor: SimpleEventTransactor{contract: contract}, SimpleEventFilterer: SimpleEventFilterer{contract: contract}}, nil
}

// NewSimpleEventCaller creates a new read-only instance of SimpleEvent, bound to a specific deployed contract.
func NewSimpleEventCaller(address common.Address, caller bind.ContractCaller) (*SimpleEventCaller, error) {
	contract, err := bindSimpleEvent(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &SimpleEventCaller{contract: contract}, nil
}

// NewSimpleEventTransactor creates a new write-only instance of SimpleEvent, bound to a specific deployed contract.
func NewSimpleEventTransactor(address common.Address, transactor bind.ContractTransactor) (*SimpleEventTransactor, error) {
	contract, err := bindSimpleEvent(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &SimpleEventTransactor{contract: contract}, nil
}

// NewSimpleEventFilterer creates a new log filterer instance of SimpleEvent, bound to a specific deployed contract.
func NewSimpleEventFilterer(address common.Address, filterer bind.ContractFilterer) (*SimpleEventFilterer, error) {
	contract, err := bindSimpleEvent(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &SimpleEventFilterer{contract: contract}, nil
}

// bindSimpleEvent binds a generic wrapper to an already deployed contract.
func bindSimpleEvent(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := SimpleEventMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_SimpleEvent *SimpleEventRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _SimpleEvent.Contract.SimpleEventCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_SimpleEvent *SimpleEventRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _SimpleEvent.Contract.SimpleEventTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_SimpleEvent *SimpleEventRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _SimpleEvent.Contract.SimpleEventTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_SimpleEvent *SimpleEventCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _SimpleEvent.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_SimpleEvent *SimpleEventTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _SimpleEvent.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_SimpleEvent *SimpleEventTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _SimpleEvent.Contract.contract.Transact(opts, method, params...)
}

// GetValue is a free data retrieval call binding the contract method 0x20965255.
//
// Solidity: function getValue() view returns(uint256)
func (_SimpleEvent *SimpleEventCaller) GetValue(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _SimpleEvent.contract.Call(opts, &out, "getValue")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetValue is a free data retrieval call binding the contract method 0x20965255.
//
// Solidity: function getValue() view returns(uint256)
func (_SimpleEvent *SimpleEventSession) GetValue() (*big.Int, error) {
	return _SimpleEvent.Contract.GetValue(&_SimpleEvent.CallOpts)
}

// GetValue is a free data retrieval call binding the contract method 0x20965255.
//
// Solidity: function getValue() view returns(uint256)
func (_SimpleEvent *SimpleEventCallerSession) GetValue() (*big.Int, error) {
	return _SimpleEvent.Contract.GetValue(&_SimpleEvent.CallOpts)
}

// SetMultipleValues is a paid mutator transaction binding the contract method 0xade8b1ed.
//
// Solidity: function setMultipleValues(uint256[] _values) returns()
func (_SimpleEvent *SimpleEventTransactor) SetMultipleValues(opts *bind.TransactOpts, _values []*big.Int) (*types.Transaction, error) {
	return _SimpleEvent.contract.Transact(opts, "setMultipleValues", _values)
}

// SetMultipleValues is a paid mutator transaction binding the contract method 0xade8b1ed.
//
// Solidity: function setMultipleValues(uint256[] _values) returns()
func (_SimpleEvent *SimpleEventSession) SetMultipleValues(_values []*big.Int) (*types.Transaction, error) {
	return _SimpleEvent.Contract.SetMultipleValues(&_SimpleEvent.TransactOpts, _values)
}

// SetMultipleValues is a paid mutator transaction binding the contract method 0xade8b1ed.
//
// Solidity: function setMultipleValues(uint256[] _values) returns()
func (_SimpleEvent *SimpleEventTransactorSession) SetMultipleValues(_values []*big.Int) (*types.Transaction, error) {
	return _SimpleEvent.Contract.SetMultipleValues(&_SimpleEvent.TransactOpts, _values)
}

// SetValue is a paid mutator transaction binding the contract method 0x55241077.
//
// Solidity: function setValue(uint256 _value) returns()
func (_SimpleEvent *SimpleEventTransactor) SetValue(opts *bind.TransactOpts, _value *big.Int) (*types.Transaction, error) {
	return _SimpleEvent.contract.Transact(opts, "setValue", _value)
}

// SetValue is a paid mutator transaction binding the contract method 0x55241077.
//
// Solidity: function setValue(uint256 _value) returns()
func (_SimpleEvent *SimpleEventSession) SetValue(_value *big.Int) (*types.Transaction, error) {
	return _SimpleEvent.Contract.SetValue(&_SimpleEvent.TransactOpts, _value)
}

// SetValue is a paid mutator transaction binding the contract method 0x55241077.
//
// Solidity: function setValue(uint256 _value) returns()
func (_SimpleEvent *SimpleEventTransactorSession) SetValue(_value *big.Int) (*types.Transaction, error) {
	return _SimpleEvent.Contract.SetValue(&_SimpleEvent.TransactOpts, _value)
}

// SimpleEventMultipleValuesChangedIterator is returned from FilterMultipleValuesChanged and is used to iterate over the raw logs and unpacked data for MultipleValuesChanged events raised by the SimpleEvent contract.
type SimpleEventMultipleValuesChangedIterator struct {
	Event *SimpleEventMultipleValuesChanged // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *SimpleEventMultipleValuesChangedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(SimpleEventMultipleValuesChanged)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(SimpleEventMultipleValuesChanged)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *SimpleEventMultipleValuesChangedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *SimpleEventMultipleValuesChangedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// SimpleEventMultipleValuesChanged represents a MultipleValuesChanged event raised by the SimpleEvent contract.
type SimpleEventMultipleValuesChanged struct {
	Sender common.Address
	Values []*big.Int
	Raw    types.Log // Blockchain specific contextual infos
}

// FilterMultipleValuesChanged is a free log retrieval operation binding the contract event 0x01bc48a284338ab1975c9290479dd8e37c372682c555dd104e5cd6be96dfe237.
//
// Solidity: event MultipleValuesChanged(address indexed sender, uint256[] values)
func (_SimpleEvent *SimpleEventFilterer) FilterMultipleValuesChanged(opts *bind.FilterOpts, sender []common.Address) (*SimpleEventMultipleValuesChangedIterator, error) {

	var senderRule []interface{}
	for _, senderItem := range sender {
		senderRule = append(senderRule, senderItem)
	}

	logs, sub, err := _SimpleEvent.contract.FilterLogs(opts, "MultipleValuesChanged", senderRule)
	if err != nil {
		return nil, err
	}
	return &SimpleEventMultipleValuesChangedIterator{contract: _SimpleEvent.contract, event: "MultipleValuesChanged", logs: logs, sub: sub}, nil
}

// WatchMultipleValuesChanged is a free log subscription operation binding the contract event 0x01bc48a284338ab1975c9290479dd8e37c372682c555dd104e5cd6be96dfe237.
//
// Solidity: event MultipleValuesChanged(address indexed sender, uint256[] values)
func (_SimpleEvent *SimpleEventFilterer) WatchMultipleValuesChanged(opts *bind.WatchOpts, sink chan<- *SimpleEventMultipleValuesChanged, sender []common.Address) (event.Subscription, error) {

	var senderRule []interface{}
	for _, senderItem := range sender {
		senderRule = append(senderRule, senderItem)
	}

	logs, sub, err := _SimpleEvent.contract.WatchLogs(opts, "MultipleValuesChanged", senderRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(SimpleEventMultipleValuesChanged)
				if err := _SimpleEvent.contract.UnpackLog(event, "MultipleValuesChanged", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseMultipleValuesChanged is a log parse operation binding the contract event 0x01bc48a284338ab1975c9290479dd8e37c372682c555dd104e5cd6be96dfe237.
//
// Solidity: event MultipleValuesChanged(address indexed sender, uint256[] values)
func (_SimpleEvent *SimpleEventFilterer) ParseMultipleValuesChanged(log types.Log) (*SimpleEventMultipleValuesChanged, error) {
	event := new(SimpleEventMultipleValuesChanged)
	if err := _SimpleEvent.contract.UnpackLog(event, "MultipleValuesChanged", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// SimpleEventValueChangedIterator is returned from FilterValueChanged and is used to iterate over the raw logs and unpacked data for ValueChanged events raised by the SimpleEvent contract.
type SimpleEventValueChangedIterator struct {
	Event *SimpleEventValueChanged // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *SimpleEventValueChangedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(SimpleEventValueChanged)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(SimpleEventValueChanged)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *SimpleEventValueChangedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *SimpleEventValueChangedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// SimpleEventValueChanged represents a ValueChanged event raised by the SimpleEvent contract.
type SimpleEventValueChanged struct {
	Sender common.Address
	Value  *big.Int
	Raw    types.Log // Blockchain specific contextual infos
}

// FilterValueChanged is a free log retrieval operation binding the contract event 0xe2b64d6eb95e02b6354c9dad0f0b75f05c8659bc5f2a0f91bc457fc27c55c688.
//
// Solidity: event ValueChanged(address indexed sender, uint256 value)
func (_SimpleEvent *SimpleEventFilterer) FilterValueChanged(opts *bind.FilterOpts, sender []common.Address) (*SimpleEventValueChangedIterator, error) {

	var senderRule []interface{}
	for _, senderItem := range sender {
		senderRule = append(senderRule, senderItem)
	}

	logs, sub, err := _SimpleEvent.contract.FilterLogs(opts, "ValueChanged", senderRule)
	if err != nil {
		return nil, err
	}
	return &SimpleEventValueChangedIterator{contract: _SimpleEvent.contract, event: "ValueChanged", logs: logs, sub: sub}, nil
}

// WatchValueChanged is a free log subscription operation binding the contract event 0xe2b64d6eb95e02b6354c9dad0f0b75f05c8659bc5f2a0f91bc457fc27c55c688.
//
// Solidity: event ValueChanged(address indexed sender, uint256 value)
func (_SimpleEvent *SimpleEventFilterer) WatchValueChanged(opts *bind.WatchOpts, sink chan<- *SimpleEventValueChanged, sender []common.Address) (event.Subscription, error) {

	var senderRule []interface{}
	for _, senderItem := range sender {
		senderRule = append(senderRule, senderItem)
	}

	logs, sub, err := _SimpleEvent.contract.WatchLogs(opts, "ValueChanged", senderRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(SimpleEventValueChanged)
				if err := _SimpleEvent.contract.UnpackLog(event, "ValueChanged", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseValueChanged is a log parse operation binding the contract event 0xe2b64d6eb95e02b6354c9dad0f0b75f05c8659bc5f2a0f91bc457fc27c55c688.
//
// Solidity: event ValueChanged(address indexed sender, uint256 value)
func (_SimpleEvent *SimpleEventFilterer) ParseValueChanged(log types.Log) (*SimpleEventValueChanged, error) {
	event := new(SimpleEventValueChanged)
	if err := _SimpleEvent.contract.UnpackLog(event, "ValueChanged", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
