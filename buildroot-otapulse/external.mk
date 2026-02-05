# OTA-Pulse external.mk
# Include all package makefiles from this BR2_EXTERNAL

include $(sort $(wildcard $(BR2_EXTERNAL_OTAPULSE_PATH)/package/*/*.mk))
