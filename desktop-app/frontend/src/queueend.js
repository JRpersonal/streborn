// queueend.js: why the last library queue ended, when the listener should
// hear it (#1890).
//
// A folder whose media server was switched off mid-album used to end with the
// player line saying "stopped" and nothing else, although the agent knew the
// server had stopped answering. The agent now names that end in /api/queue as
// lastEnd {reason: "server_gone"}, kept until STR plays anything else. The app
// shows it where "stopped" would stand, and only while the speaker is not
// playing, on the queue's own source or with none.

export const QUEUE_END_SERVER_GONE = 'server_gone';

const SHOWN_ON = new Set(['', 'UPNP', 'INVALID_SOURCE']);
const PLAYING = new Set(['PLAY_STATE', 'BUFFERING_STATE', 'PAUSE_STATE']);

// queueEndLabelKey is the i18n key of the state word to show instead of the
// usual one, '' for none. queue is the /api/queue reply, ps the play state and
// srcU the upper-cased source.
export function queueEndLabelKey(queue, ps, srcU) {
  if (!queue || queue.active) return '';
  const end = queue.lastEnd;
  if (!end || end.reason !== QUEUE_END_SERVER_GONE) return '';
  if (PLAYING.has(ps)) return '';
  if (!SHOWN_ON.has(srcU || '')) return '';
  return 'status.musicServerGone';
}
