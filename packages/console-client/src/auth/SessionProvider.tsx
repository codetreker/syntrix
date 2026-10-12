import { createContext, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from 'react';
import { createSessionController, type SessionController, type SessionState } from './controller';
import { createAuthTransport, type AuthTransport } from './transport';

type SessionContextValue = Pick<SessionController, 'login' | 'logout' | 'retry'> & { state: SessionState };
const SessionContext = createContext<SessionContextValue | null>(null);

let defaultTransport: AuthTransport | undefined;
const realTransport = (): AuthTransport => {
  defaultTransport ??= createAuthTransport();
  return defaultTransport;
};

export const SessionProvider = ({ transport, children }: { transport?: AuthTransport; children: ReactNode }) => {
  const controller = useMemo(() => createSessionController(transport ?? realTransport()), [transport]);
  const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot);
  useEffect(() => controller.connect(), [controller]);
  const value = useMemo(() => ({ state, login: controller.login, logout: controller.logout, retry: controller.retry }), [controller, state]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
};

export const useSession = (): SessionContextValue => {
  const value = useContext(SessionContext);
  if (value === null) throw new Error('useSession requires a SessionProvider');
  return value;
};
