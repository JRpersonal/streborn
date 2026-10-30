import { describe, it, expect } from 'vitest';
import { displayPreviewName } from './displaypreview.js';

describe('displayPreviewName', () => {
  it('follows the Show choice and the time', () => {
    expect(displayPreviewName(true, 'both', false)).toBe('both');
    expect(displayPreviewName(true, 'artist', true)).toBe('artist-clock');
    expect(displayPreviewName(true, 'title', null)).toBe('title');
  });
  it('shows the time alone when only the time is on', () => {
    expect(displayPreviewName(false, 'both', true)).toBe('clock');
  });
  it('has no picture when STR does not draw the panel', () => {
    expect(displayPreviewName(false, 'both', false)).toBe('');
    expect(displayPreviewName(null, 'both', null)).toBe('');
  });
  it('treats an unknown mode as both', () => {
    expect(displayPreviewName(true, 'weird', false)).toBe('both');
  });
});
