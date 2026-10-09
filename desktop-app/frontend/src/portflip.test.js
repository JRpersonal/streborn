import { describe, it, expect } from 'vitest';
import { boxRecordChange } from './boxstate.js';

// #1190: a speaker answering on both :8888 and :17008 came back on the other
// port once a minute, and the app reset its now-playing view each time.
describe('boxRecordChange', () => {
  const box = { host: '192.0.2.1', port: 8888, version: '1.0.9', friendlyName: 'Kitchen', deviceID: 'device-id-here' };

  it('reports nothing for the same record', () => {
    expect(boxRecordChange(box, { ...box })).toBe('none');
  });

  it('reports a port-only flip as port, not as a changed speaker', () => {
    expect(boxRecordChange(box, { ...box, port: 17008 })).toBe('port');
    expect(boxRecordChange({ ...box, port: 17008 }, box)).toBe('port');
  });

  it('still reports a new IP, version or name as changed', () => {
    expect(boxRecordChange(box, { ...box, host: '192.0.2.2' })).toBe('changed');
    expect(boxRecordChange(box, { ...box, version: '1.0.10' })).toBe('changed');
    expect(boxRecordChange(box, { ...box, friendlyName: 'Bath' })).toBe('changed');
    expect(boxRecordChange(box, { ...box, host: '192.0.2.2', port: 17008 })).toBe('changed');
  });

  it('treats a missing record as changed', () => {
    expect(boxRecordChange(null, box)).toBe('changed');
    expect(boxRecordChange(box, null)).toBe('changed');
  });
});
