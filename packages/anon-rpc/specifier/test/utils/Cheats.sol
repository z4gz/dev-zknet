// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @dev The few Foundry cheatcodes these tests use, declared directly instead of vendoring forge-std (the upstream
/// WorkerSpecifier test does the same). Cheatcodes live at the well-known hevm address.
interface Cheats {
    struct Log {
        bytes32[] topics;
        bytes data;
        address emitter;
    }

    function prank(address sender) external;
    function expectRevert(bytes4 selector) external;
    function recordLogs() external;
    function getRecordedLogs() external returns (Log[] memory logs);
}

Cheats constant CHEATS = Cheats(address(uint160(uint256(keccak256("hevm cheat code")))));
