# Single NIC Dual Follower Configuration

## Overview

In this configuration, ptp4l operates using the `slaveOnly yes` parameter and uses two interfaces to update a single PHC.

![dual-follower-overview](dual-follower-overview.svg)

ptp4l selects the best grandmaster based on the clock quality and priority parameters when both interfaces receive valid PTP updates. If at least one of the interfaces is in SLAVE state and locked, the reported `ptp-status/lock-state` event is LOCKED. If both interfaces are faulty or not locked, the reported `ptp-status/lock-state` event is HOLDOVER, then FREERUN once the holdover timeout expires.

![dual-follower-states](dual-follower-states.svg)

Same as with OC configuration, when ptp4l no longer receives valid updates from a grandmaster the PHC clock transitions to HOLDOVER state for the holdover duration. The realtime clock instead goes right to FREERUN.

## Failover Behavior

When the primary port loses its grandmaster, the BMCA selects the clock available on the secondary port. Reporting a FREERUN event during this transition is expected. Whether the user actually observes a FREERUN event depends on how fast the secondary port re-acquires the master clock after losing the primary and what the configured `minOffsetThreshold` / `maxOffsetThreshold` values are.

If the offset converges quickly and stays within the min/max offset thresholds, the clock will remain LOCKED without a FREERUN event being emitted. Realistically, a short transient should be expected while the clock on the second port converges. The secondary port is **not** in hot standby — it follows the BMCA, which selects only one clock at a time. Until the BMCA elects the secondary port as the new SLAVE, that port is in LISTENING state and not actively disciplining the PHC.

## Reference PtpConfig

```yaml
apiVersion: ptp.openshift.io/v1
kind: PtpConfig
metadata:
  name: ordinary-clock-1
  namespace: openshift-ptp
spec:
  profile:
  - name: oc1
    phc2sysOpts: -a -r -n 24 -N 8 -R 16 -u 0
    ptp4lConf: |-
      [ens3f2]
      masterOnly 0
      [ens3f3]
      masterOnly 0

      [global]
      #
      # Default Data Set
      #
      slaveOnly 1
      twoStepFlag 1
      priority1 128
      priority2 128
      domainNumber 24
      #utc_offset 37
      clockClass 255
      clockAccuracy 0xFE
      offsetScaledLogVariance 0xFFFF
      free_running 0
      freq_est_interval 1
      dscp_event 0
      dscp_general 0
      dataset_comparison G.8275.x
      G.8275.defaultDS.localPriority 128
      #
      # Port Data Set
      #
      logAnnounceInterval -3
      logSyncInterval -4
      logMinDelayReqInterval -4
      logMinPdelayReqInterval -4
      announceReceiptTimeout 3
      syncReceiptTimeout 0
      delayAsymmetry 0
      fault_reset_interval -4
      neighborPropDelayThresh 20000000
      masterOnly 0
      G.8275.portDS.localPriority 128
      #
      # Run time options
      #
      assume_two_step 0
      logging_level 6
      path_trace_enabled 0
      follow_up_info 0
      hybrid_e2e 0
      inhibit_multicast_service 0
      net_sync_monitor 0
      tc_spanning_tree 0
      tx_timestamp_timeout 50
      unicast_listen 0
      unicast_master_table 0
      unicast_req_duration 3600
      use_syslog 1
      verbose 1
      summary_interval -4
      kernel_leap 1
      check_fup_sync 0
      clock_class_threshold 7
      #
      # Servo Options
      #
      pi_proportional_const 0.0
      pi_integral_const 0.0
      pi_proportional_scale 0.0
      pi_proportional_exponent -0.3
      pi_proportional_norm_max 0.7
      pi_integral_scale 0.0
      pi_integral_exponent 0.4
      pi_integral_norm_max 0.3
      step_threshold 2.0
      first_step_threshold 0.00002
      max_frequency 900000000
      clock_servo pi
      sanity_freq_limit 200000000
      ntpshm_segment 0
      #
      # Transport options
      #
      transportSpecific 0x0
      ptp_dst_mac 01:1B:19:00:00:00
      p2p_dst_mac 01:80:C2:00:00:0E
      udp_ttl 1
      udp6_scope 0x0E
      uds_address /var/run/ptp4l
      #
      # Default interface options
      #
      clock_type OC
      network_transport L2
      delay_mechanism E2E
      time_stamping hardware
      tsproc_mode filter
      delay_filter moving_median
      delay_filter_length 10
      egressLatency 0
      ingressLatency 0
      boundary_clock_jbod 0
      #
      # Clock description
      #
      productDescription ;;
      revisionData ;;
      manufacturerIdentity 00:00:00
      userDescription ;
      timeSource 0xA0
    ptp4lOpts: -2  --summary_interval -4
    ptpClockThreshold:
      holdOverTimeout: 65
      maxOffsetThreshold: 100
      minOffsetThreshold: -100
    ptpSchedulingPolicy: SCHED_FIFO
    ptpSchedulingPriority: 10
  recommend:
  - match:
    - nodeLabel: node-role.kubernetes.io/worker
    priority: 4
    profile: oc1
```
