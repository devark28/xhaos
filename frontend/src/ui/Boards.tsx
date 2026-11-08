// The Boards panel: the workspace's boards, the open one marked. A click opens
// a board in the Draw tab (not on phones, which have no canvas). Rename,
// Delete and Attach work from each row, open or not. Delete asks first, in
// Xhaos's own dialog (Confirm.tsx).
import { useState, type FormEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { ConfirmDialog } from './Confirm.tsx';
import { useApp, toast } from './state/store.ts';
import { attachBoard, deleteBoard, openBoard, renameBoard } from './state/boards.ts';
import { useNarrow } from './narrow.ts';
import { boardRows, deleteBoardText } from '../board.ts';
import type { BoardSummary } from '../types.ts';

export function BoardList() {
  const boards = useApp(s => s.boards);
  const openId = useApp(s => s.boardId);
  const narrow = useNarrow();
  const [editing, setEditing] = useState<number | null>(null);
  const [draft, setDraft] = useState('');
  const [deleting, setDeleting] = useState<number | null>(null); // the board the dialog asks about
  const [busy, setBusy] = useState(false);
  const rows = boardRows(boards, openId);
  const asked = rows.find(b => b.id === deleting); // gone (deleted elsewhere): the dialog closes
  if (!rows.length) {
    return <p {...stylex.props(styles.muted)}>No boards yet. New board makes an empty one and opens it.</p>;
  }
  const save = (board: BoardSummary, event: FormEvent) => {
    event.preventDefault();
    const name = draft.trim();
    setEditing(null);
    if (!name) { toast('A board needs a name.'); return; }
    if (name !== board.name) void renameBoard(board.id, name);
  };
  const remove = async () => {
    if (!asked || busy) return;
    setBusy(true);
    try { await deleteBoard(asked.id); } finally { setBusy(false); setDeleting(null); }
  };
  return (
    <div data-board-list {...stylex.props(styles.list)}>
      {rows.map(b => {
        const open = b.open && !narrow; // a phone has no canvas, so nothing is "open" there
        return (
          <section key={b.id} data-board-row={b.id} data-open={open || undefined} {...stylex.props(styles.row, open && styles.rowOpen)}>
            {editing === b.id ? (
              <form onSubmit={e => save(b, e)} {...stylex.props(styles.line)}>
                <input aria-label="Board name" autoFocus value={draft} onChange={e => setDraft(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Escape') setEditing(null); }} {...stylex.props(styles.input)} />
                <Button type="submit" data-save-board={b.id}>Save</Button>
                <Button onClick={() => setEditing(null)}>Cancel</Button>
              </form>
            ) : (
              <div {...stylex.props(styles.line)}>
                {narrow
                  ? <span data-board-name {...stylex.props(styles.name)}>{b.name}</span>
                  : <button type="button" data-open-board={b.id} onClick={() => openBoard(b.id)} title="Open in Draw" {...stylex.props(styles.name, styles.open)}>{b.name}</button>}
                {open && <span data-open-tag {...stylex.props(styles.tag)}>open</span>}
              </div>
            )}
            <div {...stylex.props(styles.actions)}>
              <Button data-rename-board={b.id} onClick={() => { setDraft(b.name); setEditing(b.id); }}>Rename</Button>
              <Button data-delete-board={b.id} onClick={() => setDeleting(b.id)}>Delete</Button>
              <Button data-attach-png={b.id} title="Add the board to your next message as a picture" onClick={() => void attachBoard(b.id, 'png')}>Attach PNG</Button>
              <Button data-attach-scene={b.id} title="Add the board's Excalidraw data to your next message" onClick={() => void attachBoard(b.id, 'scene')}>Attach Excalidraw</Button>
            </div>
          </section>
        );
      })}
      <ConfirmDialog open={asked !== undefined} text={asked ? deleteBoardText(asked.name) : ''} action="Delete" busy={busy}
        onConfirm={() => void remove()} onCancel={() => setDeleting(null)} />
    </div>
  );
}

const styles = stylex.create({
  list: { display: 'flex', flexDirection: 'column', gap: 6 },
  muted: { color: colors.textFaint, fontSize: 12, margin: 0 },
  row: { display: 'flex', flexDirection: 'column', gap: 4, padding: 8, borderRadius: 4, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint },
  rowOpen: { borderColor: colors.brand },
  line: { display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 },
  name: { fontSize: 12, color: colors.text, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0, textAlign: 'left' },
  open: { backgroundColor: 'transparent', borderWidth: 0, padding: 0, cursor: 'pointer', color: 'inherit', fontFamily: 'inherit', textDecoration: { default: 'none', ':hover': 'underline' } },
  tag: { fontSize: 10, color: colors.brand, flexShrink: 0 },
  input: { flexGrow: 1, minWidth: 0, fontSize: 12, padding: '4px 6px', borderRadius: 4, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, backgroundColor: 'transparent', color: 'inherit' },
  actions: { display: 'flex', flexWrap: 'wrap', gap: 4, fontSize: 11 },
});
