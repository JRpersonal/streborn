import { describe, it, expect } from 'vitest';
import { sourceAccountFrom } from './nowsourceaccount.js';
import { isActiveInput } from './sourceinputs.js';

// The documents below are copied from a reporter's diagnostic, with the device
// id and speaker name already masked by the bundle itself.
const AUX_ON_SA5 =
  '<nowPlaying deviceID="DEV#6f2bf49f" source="AUX">'
  + '<ContentItem source="AUX" sourceAccount="AUX1" isPresetable="true">'
  + '<itemName>Phono</itemName></ContentItem>'
  + '<playStatus>PLAY_STATE</playStatus></nowPlaying>';

const UPNP =
  '<nowPlaying deviceID="DEV#b5877a13" source="UPNP" sourceAccount="UPnPUserName">'
  + '<ContentItem source="UPNP" location="http://192.0.2.120:50002/m/NDLNA/26797.flac"'
  + ' sourceAccount="UPnPUserName" isPresetable="false"><itemName>10 - Circle</itemName>'
  + '</ContentItem><track>10 - Circle</track></nowPlaying>';

const STANDBY =
  '<nowPlaying deviceID="DEV#7754ac21" source="STANDBY">'
  + '<ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>';

describe('sourceAccountFrom', () => {
  it('finds the socket an SA-5 only names on the ContentItem', () => {
    // The case the whole thing exists for. Reading the outer element alone
    // returns nothing here, and nothing means "matches every button".
    expect(sourceAccountFrom(AUX_ON_SA5)).toBe('AUX1');
  });

  it('prefers the outer element when it carries one', () => {
    expect(sourceAccountFrom(UPNP)).toBe('UPnPUserName');
  });

  it('answers empty when there is no account anywhere', () => {
    expect(sourceAccountFrom(STANDBY)).toBe('');
    expect(sourceAccountFrom('')).toBe('');
    expect(sourceAccountFrom(null)).toBe('');
  });

  it('does not read an account out of a value or a later element', () => {
    const tricky = '<nowPlaying source="AUX"><ContentItem source="AUX"'
      + ' itemName="not sourceAccount=&quot;WRONG&quot;" sourceAccount="AUX3" />'
      + '</nowPlaying>';
    expect(sourceAccountFrom(tricky)).toBe('AUX3');
  });
});

// The read and the decision together, because each is right on its own and the
// bug lived between them.
describe('one input lights, not three', () => {
  const buttons = [
    { source: 'AUX', sourceAccount: 'AUX1' },
    { source: 'AUX', sourceAccount: 'AUX2' },
    { source: 'AUX', sourceAccount: 'AUX3' },
  ];

  it('lights exactly the socket that is playing', () => {
    const acct = sourceAccountFrom(AUX_ON_SA5);
    const lit = buttons.filter((b) => isActiveInput(b, 'AUX', acct));
    expect(lit).toEqual([{ source: 'AUX', sourceAccount: 'AUX1' }]);
  });

  it('shows what went wrong before: no account lights all three', () => {
    // Not a wish, a record. This is what the app did, and it is why the rule
    // that treats an empty account as a match is only safe once the account is
    // actually being found.
    const lit = buttons.filter((b) => isActiveInput(b, 'AUX', ''));
    expect(lit).toHaveLength(3);
  });

  it('lights nothing when another source is playing', () => {
    const acct = sourceAccountFrom(UPNP);
    expect(buttons.filter((b) => isActiveInput(b, 'UPNP', acct))).toHaveLength(0);
  });
});
