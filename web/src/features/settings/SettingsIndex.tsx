// SPDX-License-Identifier: Apache-2.0
//
// `/settings` lands on Connections for admins and on the account page otherwise.

import { Navigate } from 'react-router-dom';
import { useMe } from '../../app/session';

export default function SettingsIndex() {
  const me = useMe();
  return <Navigate to={me.role === 'admin' ? 'connections' : 'users'} replace />;
}
