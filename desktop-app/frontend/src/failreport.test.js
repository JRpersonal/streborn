import { describe, it, expect } from 'vitest';
import { appendSavedBundlePath, failReportSaveHosts, REPORT_SEND_MARKER } from './failreport.js';

const REPORT = [
  'ST Reborn failure report',
  '========================',
  '',
  'speaker       : 192.168.1.34:8888',
  '',
  REPORT_SEND_MARKER + '. Use the',
  '"Save diagnostic logs" button on this screen to write the diagnostic',
  'file, and attach that file to the same mail.',
  '',
].join('\n');

describe('appendSavedBundlePath', () => {
  it('replaces the closing wish with the path of the file that now exists', () => {
    const out = appendSavedBundlePath(REPORT, 'C:\\Users\\koen\\str-diagnostic-ST10-20260823.zip');
    expect(out).toContain('C:\\Users\\koen\\str-diagnostic-ST10-20260823.zip');
    // The wish is gone: it named a button, not a file the user can attach.
    expect(out).not.toContain('button on this screen');
    // Everything above the closing block survives untouched.
    expect(out).toContain('speaker       : 192.168.1.34:8888');
    expect(out.indexOf(REPORT_SEND_MARKER)).toBeGreaterThan(0);
  });

  it('leaves the report alone when the user cancelled the save dialog', () => {
    expect(appendSavedBundlePath(REPORT, '')).toBe(REPORT);
    expect(appendSavedBundlePath(REPORT, null)).toBe(REPORT);
    expect(appendSavedBundlePath(REPORT, '   ')).toBe(REPORT);
  });

  it('still names the file when the closing block is not where it was', () => {
    // The Go side owns that sentence; if it is reworded past the marker the
    // path must still reach the text the user copies.
    const out = appendSavedBundlePath('a thin report with no closing block', '/tmp/str.zip');
    expect(out).toContain('/tmp/str.zip');
    expect(out).toContain('a thin report with no closing block');
  });

  it('survives an empty report', () => {
    expect(appendSavedBundlePath('', '/tmp/str.zip')).toContain('/tmp/str.zip');
    expect(appendSavedBundlePath(undefined, undefined)).toBe('');
  });
});

describe('failReportSaveHosts', () => {
  it('puts the failing speaker first and drops duplicates', () => {
    const hosts = failReportSaveHosts(
      { host: '192.168.1.34' },
      [{ host: '192.168.1.40' }, { host: '192.168.1.34' }, { host: '192.168.1.41' }],
    );
    expect(hosts).toEqual(['192.168.1.34', '192.168.1.40', '192.168.1.41']);
  });

  it('returns a usable list when the speaker never became reachable', () => {
    // A speaker that never answered has no box-side data at all. The bundle is
    // still worth writing: README + app.log are always in it.
    expect(failReportSaveHosts(null, [])).toEqual([]);
    expect(failReportSaveHosts(undefined, undefined)).toEqual([]);
    expect(failReportSaveHosts({}, [null, undefined, { host: '' }])).toEqual([]);
  });

  it('ignores non-string hosts instead of putting them in the filename', () => {
    expect(failReportSaveHosts({ host: 5 }, [{ host: {} }, { host: ' 192.168.1.9 ' }]))
      .toEqual(['192.168.1.9']);
  });
});
