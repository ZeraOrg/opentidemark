// SPDX-License-Identifier: Apache-2.0
//
// "Save as signal" (SPEC §11.4 signals, §16.3): stores the filter of the current query as a
// named signal that can be toggled in the rail.

import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { errorMessage } from '../../api/client';
import { signals } from '../../api/endpoints';
import type { Signal, Source } from '../../api/types';
import { useApi } from '../../app/services';
import { Button, Checkbox, Field, Select, TextInput } from '../../components/controls';
import { layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { TqlText } from '../../components/TqlText';

export const SIGNAL_COLORS = [
  { value: 'accent', label: 'Accent' },
  { value: 'danger', label: 'Danger' },
  { value: 'warning', label: 'Warning' },
  { value: 'info', label: 'Info' },
  { value: 'success', label: 'Success' },
  ...Array.from({ length: 8 }, (_, i) => ({ value: `chart${i + 1}`, label: `Chart ${i + 1}` })),
];

export function SaveSignalDialog({
  tql,
  source = 'logs',
  onClose,
  onSaved,
}: {
  tql: string;
  source?: Source;
  onClose: () => void;
  onSaved: (s: Signal) => void;
}) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState('');
  const [color, setColor] = useState('accent');
  const [shared, setShared] = useState(true);
  const save = useMutation({
    mutationFn: () => signals.create(api, { name: name.trim(), tql, color, shared, source }),
    onSuccess: (s) => {
      void qc.invalidateQueries({ queryKey: ['signals'] });
      toast.push({ kind: 'success', title: `Signal “${s.name}” saved` });
      onSaved(s);
      onClose();
    },
  });
  return (
    <Modal
      title="Save as signal"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={save.isPending}
            disabled={name.trim() === ''}
            onClick={() => save.mutate()}
          >
            Save signal
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <Field label="Name">
          {(id) => (
            <TextInput
              id={id}
              value={name}
              maxLength={128}
              onChange={(e) => setName(e.target.value)}
              data-autofocus
            />
          )}
        </Field>
        <Field label="Filter">{() => <TqlText q={tql} wrap />}</Field>
        <Field label="Colour">
          {(id) => (
            <Select
              id={id}
              options={SIGNAL_COLORS}
              value={color}
              onChange={(e) => setColor(e.target.value)}
            />
          )}
        </Field>
        <Checkbox checked={shared} onChange={setShared}>
          Shared with everyone
        </Checkbox>
        {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      </div>
    </Modal>
  );
}
