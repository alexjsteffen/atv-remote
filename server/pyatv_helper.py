#!/usr/bin/env python3
"""
Python helper script for pyatv operations.
This script is called by the Go WebSocket server to handle Apple TV interactions.
Communication is done via stdin/stdout using JSON.
"""
import sys
import json
import asyncio
import pyatv
import pyatv.const
from typing import Any, Optional, Union

# Disable buffering for stdout
sys.stdout = open(sys.stdout.fileno(), mode='w', buffering=1)

Protocol = pyatv.const.Protocol
pyatv_short_version = (int(pyatv.const.MAJOR_VERSION), int(pyatv.const.MINOR_VERSION))

# Global state
loop = asyncio.new_event_loop()
asyncio.set_event_loop(loop)
scan_results = {}  # identifier -> atv config
active_pairing = None
pairing_atv = None
active_device: Optional[pyatv.interface.AppleTV] = None
active_remote: Optional[pyatv.interface.RemoteControl] = None
airplay_credentials = None


def send_response(success: bool, data: Any = None, error: str = None):
    """Send a JSON response to stdout."""
    response = {"success": success}
    if data is not None:
        response["data"] = data
    if error:
        response["error"] = error
    print(json.dumps(response), flush=True)


async def do_scan() -> list:
    """Scan for Apple TV devices."""
    global scan_results
    atvs = await pyatv.scan(loop)
    results = []
    scan_results = {}
    for atv in atvs:
        device_info = {
            "name": atv.name,
            "address": str(atv.address),
            "identifier": atv.identifier
        }
        results.append(device_info)
        scan_results[atv.identifier] = atv
    return results


async def do_start_pair(identifier: str) -> bool:
    """Start pairing with AirPlay protocol."""
    global active_pairing, pairing_atv
    
    if identifier not in scan_results:
        # Re-scan to find the device
        await do_scan()
        if identifier not in scan_results:
            raise Exception(f"Device {identifier} not found")
    
    atv = scan_results[identifier]
    pairing_atv = atv
    pairing = await pyatv.pair(atv, Protocol.AirPlay, loop)
    active_pairing = pairing
    await pairing.begin()
    return True


async def do_finish_pair1(pin: str, identifier: str) -> dict:
    """Finish AirPlay pairing and start Companion pairing."""
    global active_pairing, airplay_credentials, pairing_atv
    
    pairing = active_pairing
    pairing.pin(pin)
    
    try:
        await pairing.finish()
    except pyatv.exceptions.PairingError:
        await pairing.begin()
        raise Exception("Bad PIN")
    
    if not pairing.has_paired:
        raise Exception("Did not pair with device")
    
    airplay_credentials = pairing.service.credentials
    
    # Start Companion pairing
    atv = pairing_atv
    try:
        pairing = await pyatv.pair(atv, Protocol.Companion, loop)
        active_pairing = pairing
        await pairing.begin()
    except (pyatv.exceptions.PairingError, pyatv.exceptions.NoServiceError) as e:
        raise Exception(str(e))
    
    return {
        "credentials": airplay_credentials,
        "identifier": identifier
    }


async def do_finish_pair2(pin: str, identifier: str) -> dict:
    """Finish Companion pairing."""
    global active_pairing, pairing_atv
    
    pairing = active_pairing
    pairing.pin(pin)
    
    try:
        await pairing.finish()
    except pyatv.exceptions.PairingError:
        await pairing.close()
        try:
            pairing = await pyatv.pair(pairing_atv, Protocol.Companion, loop)
            active_pairing = pairing
            await pairing.begin()
        except (pyatv.exceptions.PairingError, pyatv.exceptions.NoServiceError) as e:
            raise Exception(str(e))
        raise Exception("Bad PIN")
    
    if not pairing.has_paired:
        raise Exception("Did not pair with device")
    
    return {
        "credentials": pairing.service.credentials
    }


async def do_finish_pair(pin: str, identifier: str) -> dict:
    """Finish single-stage pairing."""
    global active_pairing, pairing_atv
    
    pairing = active_pairing
    pairing.pin(pin)
    await pairing.finish()
    
    if not pairing.has_paired:
        raise Exception("Did not pair with device")
    
    return {
        "credentials": pairing.service.credentials,
        "identifier": identifier
    }


async def do_connect(data: dict) -> dict:
    """Connect to an Apple TV."""
    global active_device, active_remote, scan_results
    
    identifier = data["identifier"]
    creds = data["credentials"]
    stored_credentials = {Protocol.AirPlay: creds}
    
    if "Companion" in data:
        stored_credentials[Protocol.Companion] = data["Companion"]
    
    atvs = await pyatv.scan(loop, identifier=identifier)
    if not atvs:
        raise Exception(f"No device found with identifier {identifier}")
    
    atv = atvs[0]
    for protocol, credentials in stored_credentials.items():
        atv.set_credentials(protocol, credentials)
    
    try:
        device = await pyatv.connect(atv, loop)
        remote = device.remote_control
        active_device = device
        active_remote = remote
        
        # Get initial power status
        power_status = "on" if device.power.power_state == pyatv.const.PowerState.On else "off"
        
        return {"connected": True, "power_status": power_status}
    except Exception as ex:
        raise Exception(f"Failed to connect: {str(ex)}")


async def do_kbfocus() -> str:
    """Get keyboard focus state."""
    global active_device
    
    if not active_device:
        return "unknown"
    
    if active_device.keyboard.text_focus_state == pyatv.const.KeyboardFocusState.Focused:
        return "focused"
    elif active_device.keyboard.text_focus_state == pyatv.const.KeyboardFocusState.Unfocused:
        return "unfocused"
    return "unknown"


async def do_settext(data: dict) -> bool:
    """Set text in keyboard."""
    global active_device
    
    if not active_device:
        return False
    
    if active_device.keyboard.text_focus_state != pyatv.const.KeyboardFocusState.Focused:
        return False
    
    text = data.get("text", "")
    await active_device.keyboard.text_set(text)
    return True


async def do_gettext() -> str:
    """Get current text from keyboard."""
    global active_device
    
    if not active_device:
        return ""
    
    if active_device.keyboard.text_focus_state != pyatv.const.KeyboardFocusState.Focused:
        return ""
    
    return await active_device.keyboard.text_get()


async def do_key(data: Union[str, dict]) -> bool:
    """Send a key press."""
    global active_device, active_remote
    
    if not active_remote:
        return False
    
    valid_keys = ['play_pause', 'left', 'right', 'down', 'up', 'select', 'menu', 
                  'top_menu', 'home', 'home_hold', 'skip_backward', 'skip_forward', 
                  'volume_up', 'volume_down']
    no_action_keys = ['play_pause', 'home_hold']
    audio_keys = ['volume_up', 'volume_down']
    
    # pyatv 0.9.0 and later uses interface.Audio
    if pyatv_short_version < (0, 9):
        no_action_keys += audio_keys
        audio_keys = []
    
    taction = None
    if isinstance(data, str):
        key = data
    else:
        key = data.get('key', '')
        if 'taction' in data:
            taction = pyatv.const.InputAction[data['taction']]
    
    if key not in valid_keys:
        return False
    
    if key in audio_keys:
        if active_device:
            await getattr(active_device.audio, key)()
    elif key in no_action_keys or not taction:
        await getattr(active_remote, key)()
    else:
        await getattr(active_remote, key)(taction)
    
    return True


async def do_power_status() -> str:
    """Get power status."""
    global active_device
    
    if not active_device:
        raise Exception("No active device")
    
    power_status = "on" if active_device.power.power_state == pyatv.const.PowerState.On else "off"
    return power_status


async def do_power_toggle() -> str:
    """Toggle power state."""
    global active_device
    
    if not active_device:
        raise Exception("No active device")
    
    if active_device.power.power_state == pyatv.const.PowerState.On:
        await active_device.power.turn_off()
        return "off"
    else:
        await active_device.power.turn_on()
        return "on"


def handle_request(request: dict):
    """Handle a single request from the Go server."""
    cmd = request.get("cmd", "")
    data = request.get("data")
    
    try:
        if cmd == "scan":
            result = loop.run_until_complete(do_scan())
            send_response(True, result)
        
        elif cmd == "startPair":
            identifier = data.get("identifier", "")
            loop.run_until_complete(do_start_pair(identifier))
            send_response(True)
        
        elif cmd == "finishPair1":
            pin = data.get("pin", "")
            identifier = data.get("identifier", "")
            result = loop.run_until_complete(do_finish_pair1(pin, identifier))
            send_response(True, result)
        
        elif cmd == "finishPair2":
            pin = data.get("pin", "")
            identifier = data.get("identifier", "")
            result = loop.run_until_complete(do_finish_pair2(pin, identifier))
            send_response(True, result)
        
        elif cmd == "finishPair":
            pin = data.get("pin", "")
            identifier = data.get("identifier", "")
            result = loop.run_until_complete(do_finish_pair(pin, identifier))
            send_response(True, result)
        
        elif cmd == "connect":
            result = loop.run_until_complete(do_connect(data))
            send_response(True, result)
        
        elif cmd == "kbfocus":
            result = loop.run_until_complete(do_kbfocus())
            send_response(True, result)
        
        elif cmd == "settext":
            result = loop.run_until_complete(do_settext(data))
            send_response(True, result)
        
        elif cmd == "gettext":
            result = loop.run_until_complete(do_gettext())
            send_response(True, result)
        
        elif cmd == "key":
            result = loop.run_until_complete(do_key(data))
            send_response(True, result)
        
        elif cmd == "power_status":
            result = loop.run_until_complete(do_power_status())
            send_response(True, result)
        
        elif cmd == "power_toggle":
            result = loop.run_until_complete(do_power_toggle())
            send_response(True, result)
        
        else:
            send_response(False, error=f"Unknown command: {cmd}")
    
    except Exception as e:
        send_response(False, error=str(e))


def main():
    """Main loop - read JSON commands from stdin, process them, and send responses."""
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        
        try:
            request = json.loads(line)
            handle_request(request)
        except json.JSONDecodeError as e:
            send_response(False, error=f"Invalid JSON: {str(e)}")
        except Exception as e:
            send_response(False, error=f"Error: {str(e)}")


if __name__ == "__main__":
    main()
