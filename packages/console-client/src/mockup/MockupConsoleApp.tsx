import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ConsoleApp } from '../app/ConsoleApp';
import { SessionProvider } from '../auth';
import { createBrowserLanguageEnvironment } from '../i18n/environment';
import { LanguageProvider } from '../i18n/provider';
import { createLanguageRuntime } from '../i18n/runtime';
import { createMockupTransport, type MockupScenario } from './transport';

const readScenario = (): MockupScenario => {
  const candidate = new URLSearchParams(window.location.search).get('scenario');
  const scenarios: MockupScenario[] = ['login', 'login-pending', 'invalid-credentials', 'login-unavailable', 'restoring', 'restore-unavailable', 'overview', 'session-expired', 'denied', 'logout-pending', 'logout-failed', 'uncertain-login', 'uncertain-session', 'language-storage'];
  return scenarios.includes(candidate as MockupScenario) ? candidate as MockupScenario : 'login';
};

export const MockupConsoleApp = () => {
  const [scenario, setScenario] = useState(readScenario);
  const scenarioRef = useRef(scenario);
  scenarioRef.current = scenario;
  const transport = useMemo(() => createMockupTransport(scenario), [scenario]);
  const [languageRuntime] = useState(() => {
    const environment = createBrowserLanguageEnvironment();
    return createLanguageRuntime({ environment: {
      ...environment,
      writePreference: (preference) => {
        if (scenarioRef.current === 'language-storage') throw new DOMException('Preview storage denied', 'SecurityError');
        environment.writePreference(preference);
      },
    } });
  });
  const navigate = useNavigate();

  useEffect(() => {
    const signedIn = ['overview', 'session-expired', 'logout-pending', 'logout-failed', 'uncertain-session'].includes(scenario);
    navigate(`${signedIn ? '/overview' : '/login'}?scenario=${scenario}`, { replace: true });
  }, [navigate, scenario]);

  return (
    <LanguageProvider runtime={languageRuntime}>
      <SessionProvider key={scenario} transport={transport}><ConsoleApp /></SessionProvider>
      <details className="mockup-toolbar">
        <summary>Interactive preview</summary>
        <label htmlFor="preview-scenario">Scenario</label>
        <select id="preview-scenario" value={scenario} onChange={(event) => setScenario(event.target.value as MockupScenario)}>
          <option value="login">Login</option>
          <option value="login-pending">Login pending</option>
          <option value="invalid-credentials">Invalid credentials</option>
          <option value="login-unavailable">Login service unavailable</option>
          <option value="restoring">Session restoring</option>
          <option value="restore-unavailable">Session restoration failed</option>
          <option value="overview">Signed-in overview</option>
          <option value="session-expired">Session expires after four seconds</option>
          <option value="denied">Request denied</option>
          <option value="logout-pending">Logout pending</option>
          <option value="logout-failed">Logout failed</option>
          <option value="uncertain-login">Login response lost, session recovered</option>
          <option value="uncertain-session">Logout response lost, session check failed</option>
          <option value="language-storage">Language choice cannot be saved</option>
        </select>
        <p>Frontend preview only. Enter any username and password, or sign out, to exercise the selected scenario.</p>
      </details>
    </LanguageProvider>
  );
};
