// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

contract SimpleEvent {
    event ValueChanged(address indexed sender, uint256 value);
    event MultipleValuesChanged(address indexed sender, uint256[] values);

    uint256 private value;

    function setValue(uint256 _value) public {
        value = _value;
        emit ValueChanged(msg.sender, _value);
    }

    function setMultipleValues(uint256[] memory _values) public {
        for (uint i = 0; i < _values.length; i++) {
            value = _values[i];
        }
        emit MultipleValuesChanged(msg.sender, _values);
    }

    function getValue() public view returns (uint256) {
        return value;
    }
}
