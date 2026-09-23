const int NUM_SLIDERS = 5;
const int analogInputs[NUM_SLIDERS] = {A0, A1, A2, A3, A4};

// both odd, so each median is a real sample rather than an interpolation.
// MEDIAN_SAMPLES rejects noise within a single read burst; HISTORY_DEPTH rejects a wiper
// losing contact for several cycles, which a burst taken inside one cycle cannot see.
// a depth of 7 discards dropouts up to 3 cycles long and costs ~46ms of lag
const int MEDIAN_SAMPLES = 5;
const int HISTORY_DEPTH = 7;

int analogSliderValues[NUM_SLIDERS];

int sampleHistory[NUM_SLIDERS][HISTORY_DEPTH];
int historyIndex = 0;
bool historyFilled = false;

void setup() { 
  for (int i = 0; i < NUM_SLIDERS; i++) {
    pinMode(analogInputs[i], INPUT);
  }

  Serial.begin(9600);
}

void loop() {
  updateSliderValues();
  sendSliderValues(); // Actually send data (all the time)
  // printSliderValues(); // For debug
  delay(10);
}

void updateSliderValues() {
  for (int i = 0; i < NUM_SLIDERS; i++) {
    // the ADC is multiplexed across every pin, so the first read after switching still
    // carries charge from the previous one. discard it, then take a median of the burst
    analogRead(analogInputs[i]);
    delayMicroseconds(20);

    int samples[MEDIAN_SAMPLES];
    for (int s = 0; s < MEDIAN_SAMPLES; s++) {
      samples[s] = analogRead(analogInputs[i]);
    }

    sampleHistory[i][historyIndex] = median(samples, MEDIAN_SAMPLES);
  }

  historyIndex++;
  if (historyIndex >= HISTORY_DEPTH) {
    historyIndex = 0;
    historyFilled = true;
  }

  // until the ring buffer has wrapped once, only the entries written so far are real
  int depth = historyFilled ? HISTORY_DEPTH : historyIndex;

  for (int i = 0; i < NUM_SLIDERS; i++) {
    int window[HISTORY_DEPTH];
    for (int h = 0; h < depth; h++) {
      window[h] = sampleHistory[i][h];
    }

    analogSliderValues[i] = median(window, depth);
  }
}

// insertion sort the values and return the middle one
int median(int *values, int count) {
  for (int i = 1; i < count; i++) {
    int value = values[i];
    int j = i - 1;

    while (j >= 0 && values[j] > value) {
      values[j + 1] = values[j];
      j--;
    }

    values[j + 1] = value;
  }

  return values[count / 2];
}

void sendSliderValues() {
  String builtString = String("");

  for (int i = 0; i < NUM_SLIDERS; i++) {
    builtString += String((int)analogSliderValues[i]);

    if (i < NUM_SLIDERS - 1) {
      builtString += String("|");
    }
  }
  
  Serial.println(builtString);
}

void printSliderValues() {
  for (int i = 0; i < NUM_SLIDERS; i++) {
    String printedString = String("Slider #") + String(i + 1) + String(": ") + String(analogSliderValues[i]) + String(" mV");
    Serial.write(printedString.c_str());

    if (i < NUM_SLIDERS - 1) {
      Serial.write(" | ");
    } else {
      Serial.write("\n");
    }
  }
}
