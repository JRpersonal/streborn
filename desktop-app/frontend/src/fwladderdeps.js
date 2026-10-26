// The firmware ladder's backend calls, in one place for every screen that
// shows the firmware guide (setup and speaker settings). fwladder.js itself
// takes them as a parameter so its tests run without Wails.
import {
  CancelFirmwareStick,
  CheckStick,
  EventsOn,
  FormatStick,
  GetFirmwareLadder,
  GetFirmwareLadderManual,
  ListDrives,
  PickFirmwareFile,
  PrepareFirmwareStick,
  PrepareFirmwareStickFromFile,
  RecheckFirmware,
} from './api.js';

export function firmwareLadderDeps() {
  return {
    getLadder: GetFirmwareLadder,
    getLadderManual: GetFirmwareLadderManual,
    prepareStick: PrepareFirmwareStick,
    prepareStickFromFile: PrepareFirmwareStickFromFile,
    pickFile: PickFirmwareFile,
    cancelStick: CancelFirmwareStick,
    recheck: RecheckFirmware,
    listDrives: ListDrives,
    checkStick: CheckStick,
    formatStick: FormatStick,
    onProgress: (cb) => EventsOn('fwladder:progress', cb),
  };
}
