import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Button,
  Card,
  Checkbox,
  Icon,
  Label,
  Message,
  Segment,
  Table,
  Popup,
} from 'semantic-ui-react';
import { useTranslation } from 'react-i18next';
import { API, showError, showSuccess, timestamp2string } from '../../helpers';

const REFRESH_INTERVAL_MS = 5000;

// Channel status constants (mirrors model/channel.go).
const STATUS_ENABLED = 1;
const STATUS_MANUAL_DISABLED = 2;
const STATUS_AUTO_DISABLED = 3;

function statusBadge(status, t) {
  switch (status) {
    case STATUS_ENABLED:
      return (
        <Label basic color='green' size='tiny'>
          <Icon name='check' /> {t('routing.healthy')}
        </Label>
      );
    case STATUS_MANUAL_DISABLED:
      return (
        <Label basic color='red' size='tiny'>
          <Icon name='ban' /> {t('routing.manual_disabled')}
        </Label>
      );
    case STATUS_AUTO_DISABLED:
      return (
        <Label basic color='orange' size='tiny'>
          <Icon name='warning sign' /> {t('routing.auto_disabled')}
        </Label>
      );
    default:
      return (
        <Label basic size='tiny'>
          {t('routing.unknown')}
        </Label>
      );
  }
}

function cooldownBadge(until, t) {
  if (!until) return null;
  return (
    <Label basic color='orange' size='tiny'>
      <Icon name='clock' /> {t('routing.rate_limited')}
    </Label>
  );
}

function quotaBadge(balance, t) {
  if (balance === 0 || balance === undefined) return null;
  if (balance < 0.01) {
    return (
      <Label basic color='red' size='tiny'>
        <Icon name='warning' /> {t('routing.quota_exhausted')}
      </Label>
    );
  }
  if (balance < 1) {
    return (
      <Label basic color='yellow' size='tiny'>
        ${balance.toFixed(2)}
      </Label>
    );
  }
  return <span>${balance.toFixed(2)}</span>;
}

function responseTimeMs(ms) {
  if (!ms || ms <= 0) return '—';
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`;
  return `${ms}ms`;
}

const Routing = () => {
  const { t } = useTranslation();
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const timerRef = useRef(null);

  const load = useCallback(async () => {
    try {
      const res = await API.get('/api/routing/status');
      const { success, message, data } = res.data;
      if (success) {
        setData(data);
      } else {
        showError(message);
      }
    } catch (err) {
      showError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    return () => {
      if (timerRef.current) {
        clearInterval(timerRef.current);
      }
    };
  }, [load]);

  useEffect(() => {
    if (timerRef.current) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
    if (autoRefresh) {
      timerRef.current = setInterval(load, REFRESH_INTERVAL_MS);
    }
  }, [autoRefresh, load]);

  async function unbind(sessionKey) {
    try {
      const res = await API.delete('/api/routing/session', {
        data: { session_key: sessionKey },
      });
      const { success, message } = res.data;
      if (success) {
        showSuccess(t('routing.unbind_success'));
        await load();
      } else {
        showError(message);
      }
    } catch (err) {
      showError(err.message);
    }
  }

  async function clearAll() {
    try {
      const res = await API.delete('/api/routing/sessions');
      const { success, message } = res.data;
      if (success) {
        showSuccess(t('routing.clear_success'));
        await load();
      } else {
        showError(message);
      }
    } catch (err) {
      showError(err.message);
    }
  }

  const sessions = data ? data.sessions : [];
  const channels = data ? data.channels : [];
  const channelNames = data ? data.channel_names || {} : {};
  const totalRequests = sessions.reduce((sum, s) => sum + s.requests, 0);

  function channelLabel(id) {
    const name = channelNames[id];
    if (name) {
      return `${name} (#${id})`;
    }
    return `#${id}`;
  }

  return (
    <div className='dashboard-container'>
      <Card fluid className='chart-card'>
        <Card.Content>
          <Card.Header className='header'>
            {t('routing.title')}
            <div style={{ float: 'right' }}>
              <Checkbox
                toggle
                checked={autoRefresh}
                onChange={(e, { checked }) => setAutoRefresh(checked)}
                label={t('routing.auto_refresh')}
              />
              <Button
                style={{ marginLeft: '12px' }}
                size='small'
                onClick={() => load()}
                loading={loading}
              >
                <Icon name='refresh' />
                {t('routing.refresh')}
              </Button>
              <Button
                color='red'
                size='small'
                style={{ marginLeft: '8px' }}
                onClick={clearAll}
                disabled={sessions.length === 0}
              >
                <Icon name='trash' />
                {t('routing.clear_all')}
              </Button>
            </div>
          </Card.Header>

          {data && (
            <Segment basic>
              <Label
                color={data.enabled ? 'green' : 'grey'}
                size='large'
                basic
              >
                <Icon name={data.enabled ? 'check circle' : 'ban'} />
                {data.enabled ? t('routing.enabled') : t('routing.disabled')}
              </Label>
              <Label size='large' basic>
                <Icon name='cubes' />
                {t('routing.models')}: {data.models.join(', ')}
              </Label>
              <Label size='large' basic>
                <Icon name='hourglass half' />
                {t('routing.cooldown_seconds')}: {data.cooldown_seconds}
              </Label>
              <Label size='large' basic>
                <Icon name='clock outline' />
                {t('routing.session_ttl_seconds')}: {data.session_ttl_seconds}
              </Label>
              <Label size='large' basic>
                <Icon name='sliders horizontal' />
                {t('routing.session_id_header')}: {data.session_id_header}
              </Label>
              <Label size='large' basic>
                <Icon name='shield' />
                {t('routing.failure_threshold')}: {data.failure_threshold}
              </Label>
            </Segment>
          )}

          {data && (
            <Message info>
              {t('routing.statistics')}:{' '}
              <b>{sessions.length}</b> {t('routing.active_sessions')},{' '}
              <b>{totalRequests}</b> {t('routing.total_requests')},{' '}
              <b>{channels.length}</b> {t('routing.active_channels')}
            </Message>
          )}

          {/* ---- Channel health table ---- */}
          <Table basic='very' compact celled striped>
            <Table.Header>
              <Table.Row>
                <Table.HeaderCell>{t('routing.channel_header')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.status_label')}</Table.HeaderCell>
                <Table.HeaderCell textAlign='center'>{t('routing.active_sessions')}</Table.HeaderCell>
                <Table.HeaderCell textAlign='center'>{t('routing.requests')}</Table.HeaderCell>
                <Table.HeaderCell textAlign='center'>{t('routing.failures')}</Table.HeaderCell>
                <Table.HeaderCell textAlign='center'>{t('routing.response_time')}</Table.HeaderCell>
                <Table.HeaderCell textAlign='center'>{t('routing.quota')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.channel_state')}</Table.HeaderCell>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {channels.map((ch) => (
                <Table.Row
                  key={ch.channel_id}
                  style={
                    ch.status !== STATUS_ENABLED
                      ? { opacity: 0.55 }
                      : undefined
                  }
                >
                  <Table.Cell>
                    <span style={{ fontWeight: 500 }}>
                      {ch.name || channelLabel(ch.channel_id)}
                    </span>
                    <span style={{ color: '#999', marginLeft: 6, fontSize: '0.85em' }}>
                      #{ch.channel_id}
                    </span>
                  </Table.Cell>
                  <Table.Cell>
                    {statusBadge(ch.status, t)}
                    {cooldownBadge(ch.cooling_until, t)}
                  </Table.Cell>
                  <Table.Cell textAlign='center'>{ch.sessions}</Table.Cell>
                  <Table.Cell textAlign='center'>{ch.requests}</Table.Cell>
                  <Table.Cell textAlign='center'>
                    {ch.failures > 0 ? (
                      <Label basic color='orange' size='tiny'>
                        {ch.failures}
                      </Label>
                    ) : (
                      0
                    )}
                  </Table.Cell>
                  <Table.Cell textAlign='center'>
                    {responseTimeMs(ch.response_time)}
                  </Table.Cell>
                  <Table.Cell textAlign='center'>
                    {quotaBadge(ch.balance, t)}
                  </Table.Cell>
                  <Table.Cell>
                    <Popup
                      content={`Busyness: ${ch.busyness.toFixed(1)}`}
                      trigger={
                        <Icon
                          name={
                            ch.busyness > 0
                              ? 'arrow up'
                              : ch.busyness < 0
                              ? 'arrow down'
                              : 'minus'
                          }
                          color={
                            ch.busyness > 0
                              ? 'red'
                              : ch.busyness < 0
                              ? 'grey'
                              : 'grey'
                          }
                        />
                      }
                      size='tiny'
                      position='top center'
                    />
                  </Table.Cell>
                </Table.Row>
              ))}
              {channels.length === 0 && (
                <Table.Row>
                  <Table.Cell colSpan='8' textAlign='center'>
                    {t('routing.no_channels')}
                  </Table.Cell>
                </Table.Row>
              )}
            </Table.Body>
          </Table>

          {/* ---- Sessions table ---- */}
          <Table basic='very' compact>
            <Table.Header>
              <Table.Row>
                <Table.HeaderCell>{t('routing.session_key')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.model')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.group')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.channel_id')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.requests')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.failures')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.consecutive_failures')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.last_seen')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.action')}</Table.HeaderCell>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {sessions.map((s) => (
                <Table.Row key={`${s.session_key}-${s.model}-${s.channel_id}`}>
                  <Table.Cell>
                    <code>{s.session_key}</code>
                  </Table.Cell>
                  <Table.Cell>{s.model}</Table.Cell>
                  <Table.Cell>{s.group}</Table.Cell>
                  <Table.Cell>{channelLabel(s.channel_id)}</Table.Cell>
                  <Table.Cell>{s.requests}</Table.Cell>
                  <Table.Cell>
                    {s.failures > 0 ? (
                      <Label basic color='orange'>
                        {s.failures}
                      </Label>
                    ) : (
                      0
                    )}
                  </Table.Cell>
                  <Table.Cell>
                    {s.consecutive_failures > 0 ? (
                      <Label
                        basic
                        color={s.consecutive_failures >= (data.failure_threshold || 3) ? 'red' : 'yellow'}
                        size='tiny'
                      >
                        {s.consecutive_failures}
                      </Label>
                    ) : (
                      0
                    )}
                  </Table.Cell>
                  <Table.Cell>{timestamp2string(s.last_seen)}</Table.Cell>
                  <Table.Cell>
                    <Button
                      size='mini'
                      color='blue'
                      onClick={() => unbind(s.session_key)}
                    >
                      <Icon name='unlinkify' />
                      {t('routing.unbind')}
                    </Button>
                  </Table.Cell>
                </Table.Row>
              ))}
              {sessions.length === 0 && (
                <Table.Row>
                  <Table.Cell colSpan='9' textAlign='center'>
                    {t('routing.no_sessions')}
                  </Table.Cell>
                </Table.Row>
              )}
            </Table.Body>
          </Table>
        </Card.Content>
      </Card>
    </div>
  );
};

export default Routing;
