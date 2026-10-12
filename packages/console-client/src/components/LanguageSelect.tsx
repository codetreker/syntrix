import { Trans } from '@lingui/react/macro';
import { useId } from 'react';
import { useLanguage } from '../i18n/provider';

export const LanguageSelect = () => {
  const id = useId();
  const { preference, setPreference, persistenceError } = useLanguage();

  return (
    <div className="language-control">
      <div className="language-select">
        <label htmlFor={id}><Trans>Language</Trans></label>
        <select id={id} value={preference} aria-describedby={persistenceError ? `${id}-warning` : undefined} onChange={(event) => setPreference(event.target.value as typeof preference)}>
          <option value="system"><Trans>Follow system</Trans></option>
          <option value="en" lang="en">English</option>
          <option value="zh-CN" lang="zh-CN">简体中文</option>
        </select>
      </div>
      {persistenceError && (
        <p className="language-warning" role="status" id={`${id}-warning`}>
          {persistenceError.operation === 'read'
            ? <Trans>Your saved language could not be read. Your current language remains available.</Trans>
            : <Trans>Your language choice could not be saved in this browser. It applies to this page.</Trans>}
        </p>
      )}
    </div>
  );
};
